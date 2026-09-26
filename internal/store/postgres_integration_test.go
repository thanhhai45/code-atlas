//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/thanhhai45/code-atlas/internal/model"
)

// Needs PostgreSQL: DATABASE_URL, or the docker compose default.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://atlas:atlas@localhost:5432/atlas?sslmode=disable"
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func testRepo(id int64, fullName string) model.Repository {
	now := time.Now().UTC().Truncate(time.Second)
	return model.Repository{ID: id, Name: "x", FullName: fullName, Owner: "it-test", URL: "https://github.com/" + fullName,
		CreatedAt: now, UpdatedAt: now, PushedAt: now}
}

// A repository name can move to another GitHub id (rename, transfer, delete and
// re-create, or sample data with synthetic ids). The upsert must hand the name
// to the new id instead of failing the whole batch on the unique name index.
func TestUpsertMovesNameToNewID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const stale, fresh, other = 990_000_001, 990_000_002, 990_000_003
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM repositories WHERE github_id IN ($1, $2, $3)`, stale, fresh, other)
	})

	if _, err := s.UpsertRepositories(ctx, []model.Repository{testRepo(stale, "it-test/Moved"), testRepo(other, "it-test/other")}); err != nil {
		t.Fatal(err)
	}
	// Same name, different case, new id.
	displaced, err := s.UpsertRepositories(ctx, []model.Repository{testRepo(fresh, "IT-test/moved"), testRepo(other, "it-test/other")})
	if err != nil {
		t.Fatalf("upsert must not fail on a name held by another id: %v", err)
	}
	if !slices.Equal(displaced, []int64{stale}) {
		t.Errorf("displaced = %v, want [%d]", displaced, stale)
	}
	if _, err := s.GetRepository(ctx, stale); !errors.Is(err, ErrNotFound) {
		t.Errorf("stale row must be gone, got err=%v", err)
	}
	if r, err := s.GetRepository(ctx, fresh); err != nil || r.FullName != "IT-test/moved" {
		t.Errorf("new row: %+v, %v", r, err)
	}

	// Re-running the same batch changes nothing.
	displaced, err = s.UpsertRepositories(ctx, []model.Repository{testRepo(fresh, "IT-test/moved")})
	if err != nil || len(displaced) != 0 {
		t.Errorf("idempotent re-run: displaced=%v err=%v", displaced, err)
	}
}
