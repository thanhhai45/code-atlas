// Package store persists repositories in PostgreSQL, the system of record.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thanhhai45/code-atlas/internal/model"
)

//go:embed migrations/*.sql
var migrations embed.FS

var ErrNotFound = errors.New("not found")

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies embedded SQL migrations in lexical order, once each.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

const upsertSQL = `
INSERT INTO repositories (
    github_id, name, full_name, owner, description, url, homepage, language, topics,
    license, license_name, stars, forks, watchers, open_issues, default_branch,
    archived, fork, readme, created_at, updated_at, pushed_at, synced_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22, now())
ON CONFLICT (github_id) DO UPDATE SET
    name = EXCLUDED.name, full_name = EXCLUDED.full_name, owner = EXCLUDED.owner,
    description = EXCLUDED.description, url = EXCLUDED.url, homepage = EXCLUDED.homepage,
    language = EXCLUDED.language, topics = EXCLUDED.topics, license = EXCLUDED.license,
    license_name = EXCLUDED.license_name, stars = EXCLUDED.stars, forks = EXCLUDED.forks,
    watchers = EXCLUDED.watchers, open_issues = EXCLUDED.open_issues,
    default_branch = EXCLUDED.default_branch, archived = EXCLUDED.archived, fork = EXCLUDED.fork,
    -- keep a previously fetched README when this sync did not fetch one
    readme = CASE WHEN EXCLUDED.readme = '' THEN repositories.readme ELSE EXCLUDED.readme END,
    created_at = EXCLUDED.created_at, updated_at = EXCLUDED.updated_at,
    pushed_at = EXCLUDED.pushed_at, synced_at = now()`

// UpsertRepositories writes a batch idempotently: re-running a crawl never duplicates rows.
func (s *Store) UpsertRepositories(ctx context.Context, repos []model.Repository) error {
	batch := &pgx.Batch{}
	for _, r := range repos {
		batch.Queue(upsertSQL,
			r.ID, r.Name, r.FullName, r.Owner, r.Description, r.URL, r.Homepage, r.Language, nonNil(r.Topics),
			r.License, r.LicenseName, r.Stars, r.Forks, r.Watchers, r.OpenIssues, r.DefaultBranch,
			r.Archived, r.Fork, model.TruncateReadme(r.Readme), r.CreatedAt, r.UpdatedAt, r.PushedAt)
	}
	return s.pool.SendBatch(ctx, batch).Close()
}

const selectColumns = `github_id, name, full_name, owner, description, url, homepage, language, topics,
    license, license_name, stars, forks, watchers, open_issues, default_branch, archived, fork,
    readme, categories, technologies, use_cases, created_at, updated_at, pushed_at`

func scanRepository(row pgx.Row) (model.Repository, error) {
	var r model.Repository
	err := row.Scan(&r.ID, &r.Name, &r.FullName, &r.Owner, &r.Description, &r.URL, &r.Homepage,
		&r.Language, &r.Topics, &r.License, &r.LicenseName, &r.Stars, &r.Forks, &r.Watchers,
		&r.OpenIssues, &r.DefaultBranch, &r.Archived, &r.Fork, &r.Readme, &r.Categories,
		&r.Technologies, &r.UseCases, &r.CreatedAt, &r.UpdatedAt, &r.PushedAt)
	return r, err
}

func (s *Store) GetRepository(ctx context.Context, id int64) (model.Repository, error) {
	r, err := scanRepository(s.pool.QueryRow(ctx, `SELECT `+selectColumns+` FROM repositories WHERE github_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// ListRepositories returns a page ordered by stars. It omits README bodies to keep payloads small.
func (s *Store) ListRepositories(ctx context.Context, limit, offset int) ([]model.Repository, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM repositories`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+selectColumns+` FROM repositories
		ORDER BY stars DESC, github_id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.Repository{}
	for rows.Next() {
		r, err := scanRepository(rows)
		if err != nil {
			return nil, 0, err
		}
		r.Readme = ""
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// StreamRepositories walks the whole table in primary-key order (keyset pagination,
// not OFFSET) and hands batches to fn. Used to rebuild the search index.
func (s *Store) StreamRepositories(ctx context.Context, batchSize int, fn func([]model.Repository) error) error {
	var after int64 = -1
	for {
		rows, err := s.pool.Query(ctx, `SELECT `+selectColumns+` FROM repositories
			WHERE github_id > $1 ORDER BY github_id LIMIT $2`, after, batchSize)
		if err != nil {
			return err
		}
		batch := make([]model.Repository, 0, batchSize)
		for rows.Next() {
			r, err := scanRepository(rows)
			if err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		if err := fn(batch); err != nil {
			return err
		}
		after = batch[len(batch)-1].ID
	}
}

type CrawlRun struct {
	ID      int64
	Fetched int
	Indexed int
	Failed  int
}

func (s *Store) StartCrawlRun(ctx context.Context, query string) (*CrawlRun, error) {
	run := &CrawlRun{}
	err := s.pool.QueryRow(ctx, `INSERT INTO crawl_runs (query) VALUES ($1) RETURNING id`, query).Scan(&run.ID)
	return run, err
}

func (s *Store) FinishCrawlRun(ctx context.Context, run *CrawlRun, runErr error) error {
	status, msg := "succeeded", ""
	if runErr != nil {
		status, msg = "failed", runErr.Error()
	} else if run.Failed > 0 {
		status = "partial"
	}
	_, err := s.pool.Exec(ctx, `UPDATE crawl_runs SET status=$2, fetched=$3, indexed=$4, failed=$5,
		error=$6, finished_at=$7 WHERE id=$1`, run.ID, status, run.Fetched, run.Indexed, run.Failed, msg, time.Now())
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
