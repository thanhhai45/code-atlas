// Command crawler ingests GitHub repositories into PostgreSQL and Elasticsearch.
//
//	crawler -min-stars 1000 -max 10000            # crawl GitHub search
//	crawler -q "language:go topic:database"       # narrow the crawl
//	crawler -seed testdata/seed_repositories.json # load sample data offline
//	crawler -reindex                              # rebuild the index from PostgreSQL
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/thanhhai45/code-atlas/internal/config"
	"github.com/thanhhai45/code-atlas/internal/github"
	"github.com/thanhhai45/code-atlas/internal/model"
	"github.com/thanhhai45/code-atlas/internal/search"
	"github.com/thanhhai45/code-atlas/internal/store"
)

type options struct {
	qualifiers string
	minStars   int
	max        int
	readme     bool
	workers    int
	seed       string
	reindex    bool
	keepOld    bool
	batchSize  int
}

func main() {
	var o options
	flag.StringVar(&o.qualifiers, "q", "", `extra GitHub search qualifiers, e.g. "language:go topic:database"`)
	flag.IntVar(&o.minStars, "min-stars", 100, "minimum stars")
	flag.IntVar(&o.max, "max", 1000, "maximum repositories to fetch (0 = no limit)")
	flag.BoolVar(&o.readme, "readme", false, "also fetch READMEs (one extra API call per repository)")
	flag.IntVar(&o.workers, "workers", 4, "concurrent README fetchers")
	flag.StringVar(&o.seed, "seed", "", "load repositories from a JSON file instead of GitHub")
	flag.BoolVar(&o.reindex, "reindex", false, "rebuild the search index from PostgreSQL into a new versioned index and swap the alias")
	flag.BoolVar(&o.keepOld, "keep-old", false, "with -reindex: keep the previous index instead of deleting it")
	flag.IntVar(&o.batchSize, "batch", 500, "bulk indexing batch size")
	flag.Parse()

	if err := run(o); err != nil {
		slog.Error("crawler failed", "err", err)
		os.Exit(1)
	}
}

func run(o options) error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	es := search.NewClient(cfg.ElasticsearchURL, cfg.SearchAlias)
	if err := es.EnsureIndex(ctx); err != nil {
		return fmt.Errorf("ensure index: %w", err)
	}

	if o.reindex {
		return reindex(ctx, db, es, o)
	}

	label := fmt.Sprintf("github: %q min-stars=%d max=%d", o.qualifiers, o.minStars, o.max)
	if o.seed != "" {
		label = "seed: " + o.seed
	}
	run, err := db.StartCrawlRun(ctx, label)
	if err != nil {
		return err
	}
	start := time.Now()
	sink := func(repos []model.Repository) error { return ingest(ctx, db, es, run, repos) }

	if o.seed != "" {
		err = loadSeed(o.seed, sink)
	} else {
		gh := github.NewClient(cfg.GitHubAPIURL, cfg.GitHubToken)
		if cfg.GitHubToken == "" {
			slog.Warn("GITHUB_TOKEN not set: unauthenticated search is limited to 10 requests/minute")
		}
		if o.readme {
			sink = withReadmes(ctx, gh, o.workers, sink)
		}
		_, err = gh.SearchAll(ctx, github.SearchOptions{Qualifiers: o.qualifiers, MinStars: o.minStars, Max: o.max}, sink)
	}
	if ferr := db.FinishCrawlRun(context.WithoutCancel(ctx), run, err); ferr != nil {
		slog.Warn("could not record crawl run", "err", ferr)
	}
	slog.Info("crawl finished", "fetched", run.Fetched, "indexed", run.Indexed, "failed", run.Failed,
		"duration", time.Since(start).Round(time.Millisecond))
	return err
}

// ingest writes a batch to PostgreSQL first (source of truth), then to Elasticsearch.
// An indexing failure is counted but does not abort the crawl: the documents can be
// recovered later with -reindex because PostgreSQL already has them.
func ingest(ctx context.Context, db *store.Store, es *search.Client, run *store.CrawlRun, repos []model.Repository) error {
	run.Fetched += len(repos)
	if err := db.UpsertRepositories(ctx, repos); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	res, err := es.BulkIndex(ctx, "", repos)
	if err != nil {
		run.Failed += len(repos)
		slog.Error("bulk index failed", "count", len(repos), "err", err)
		return nil
	}
	run.Indexed += res.Indexed
	run.Failed += res.Failed
	for _, e := range res.Errors {
		slog.Warn("document failed to index", "detail", e)
	}
	slog.Info("batch ingested", "batch", len(repos), "total_fetched", run.Fetched, "total_indexed", run.Indexed)
	return nil
}

// withReadmes decorates a sink so each batch gets its READMEs fetched concurrently.
func withReadmes(ctx context.Context, gh *github.Client, workers int, next func([]model.Repository) error) func([]model.Repository) error {
	return func(repos []model.Repository) error {
		jobs := make(chan int)
		var wg sync.WaitGroup
		for range max(1, workers) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					readme, err := gh.Readme(ctx, repos[i].FullName)
					if err != nil {
						slog.Warn("readme fetch failed", "repo", repos[i].FullName, "err", err)
						continue
					}
					repos[i].Readme = readme
				}
			}()
		}
		for i := range repos {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
		return next(repos)
	}
}

func loadSeed(path string, sink func([]model.Repository) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var repos []model.Repository
	if err := json.Unmarshal(data, &repos); err != nil {
		return fmt.Errorf("parse seed: %w", err)
	}
	if len(repos) == 0 {
		return errors.New("seed file is empty")
	}
	return sink(repos)
}

// reindex builds a brand-new index from PostgreSQL and atomically swaps the alias,
// so searches keep being served from the old index until the new one is complete.
func reindex(ctx context.Context, db *store.Store, es *search.Client, o options) error {
	old, err := es.AliasTargets(ctx)
	if err != nil {
		return err
	}
	name := es.NewIndexName(time.Now())
	slog.Info("reindex: creating index", "index", name, "previous", old)
	if err := es.CreateIndex(ctx, name); err != nil {
		return err
	}
	total, failed := 0, 0
	err = db.StreamRepositories(ctx, o.batchSize, func(batch []model.Repository) error {
		res, err := es.BulkIndex(ctx, name, batch)
		if err != nil {
			return err
		}
		total += res.Indexed
		failed += res.Failed
		slog.Info("reindex: batch", "indexed", total, "failed", failed)
		return nil
	})
	if err == nil && failed > 0 {
		err = fmt.Errorf("%d documents failed to index", failed)
	}
	if err != nil {
		slog.Error("reindex aborted; alias unchanged", "err", err)
		_ = es.DeleteIndex(context.WithoutCancel(ctx), name)
		return err
	}
	if err := es.Refresh(ctx, name); err != nil {
		return err
	}
	if err := es.SwapAlias(ctx, name, old); err != nil {
		return err
	}
	slog.Info("reindex: alias swapped", "alias", es.Alias(), "index", name, "documents", total)
	if !o.keepOld {
		for _, idx := range old {
			if err := es.DeleteIndex(ctx, idx); err != nil {
				slog.Warn("could not delete old index", "index", idx, "err", err)
			}
		}
	}
	return nil
}
