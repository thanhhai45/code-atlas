// Command datagen loads synthetic repositories into Elasticsearch for
// benchmarking, and measures indexing throughput while doing so.
//
//	datagen -n 100000                      # 100K docs into the "repositories_bench" alias
//	datagen -n 1000000 -vectors=false      # 1M docs without embeddings
//	datagen -n 1000000 -shards 3 -alias repositories_bench_s3 -force-merge
//
// It writes a new versioned index behind its own alias (never the live
// "repositories" alias), with replicas and refresh disabled during the load,
// then restores them, refreshes and swaps the alias.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thanhhai45/code-atlas/internal/config"
	"github.com/thanhhai45/code-atlas/internal/embed"
	"github.com/thanhhai45/code-atlas/internal/model"
	"github.com/thanhhai45/code-atlas/internal/search"
	"github.com/thanhhai45/code-atlas/internal/synth"
)

type Report struct {
	Index         string            `json:"index"`
	Alias         string            `json:"alias"`
	Shards        int               `json:"shards"`
	Docs          int               `json:"docs"`
	Failed        int               `json:"failed"`
	Vectors       bool              `json:"vectors"`
	Readme        bool              `json:"readme"`
	BatchSize     int               `json:"batch_size"`
	Workers       int               `json:"workers"`
	LoadSeconds   float64           `json:"load_seconds"`
	DocsPerSec    float64           `json:"docs_per_sec"`
	RefreshSecs   float64           `json:"refresh_seconds"`
	MergeSecs     float64           `json:"force_merge_seconds,omitempty"`
	IndexStats    search.IndexStats `json:"index_stats"`
	BytesPerDoc   float64           `json:"bytes_per_doc"`
	ElasticURL    string            `json:"-"`
	GeneratorSeed uint64            `json:"seed"`
}

func main() {
	n := flag.Int("n", 100_000, "number of repositories to generate")
	alias := flag.String("alias", "repositories_bench", "alias to load into (never use the live alias)")
	batch := flag.Int("batch", 1000, "documents per _bulk request")
	workers := flag.Int("workers", 4, "concurrent _bulk requests")
	vectors := flag.Bool("vectors", true, "generate topic-clustered embeddings (384 dims)")
	readme := flag.Bool("readme", true, "generate README bodies")
	seed := flag.Uint64("seed", 1, "generator seed")
	keepOld := flag.Bool("keep-old", false, "keep the index previously behind the alias")
	shards := flag.Int("shards", 1, "primary shards of the new index")
	forceMerge := flag.Bool("force-merge", false, "force-merge every shard to one segment after the load")
	jsonOut := flag.String("json", "", "write the report to this file")
	flag.Parse()

	opts := options{
		n: *n, alias: *alias, batchSize: *batch, workers: *workers, vectors: *vectors, readme: *readme,
		seed: *seed, keepOld: *keepOld, shards: *shards, forceMerge: *forceMerge, jsonOut: *jsonOut,
	}
	if err := run(opts); err != nil {
		slog.Error("datagen failed", "err", err)
		os.Exit(1)
	}
}

type options struct {
	n, batchSize, workers, shards int
	alias, jsonOut                string
	vectors, readme, keepOld      bool
	forceMerge                    bool
	seed                          uint64
}

func run(o options) error {
	n, alias, batchSize, workers, vectors, readme, seed := o.n, o.alias, o.batchSize, o.workers, o.vectors, o.readme, o.seed
	cfg := config.Load()
	if alias == cfg.SearchAlias {
		return fmt.Errorf("refusing to overwrite the live alias %q; pick another -alias", alias)
	}
	ctx := context.Background()
	es := search.NewClient(cfg.ElasticsearchURL, alias)

	old, err := es.AliasTargets(ctx)
	if err != nil {
		return err
	}
	index := es.NewIndexName(time.Now())
	if err := es.CreateIndexWithShards(ctx, index, o.shards); err != nil {
		return err
	}
	// Bulk-load settings: no replicas to copy to, no refreshes to pay for.
	if err := es.UpdateSettings(ctx, index, map[string]any{"refresh_interval": "-1", "number_of_replicas": 0}); err != nil {
		return err
	}

	gen := synth.New(seed, embed.Dim)
	gen.Vectors, gen.Readme = vectors, readme

	batches := make(chan [2]int)
	var indexed, failed atomic.Int64
	var firstErr error
	var errOnce sync.Once
	var wg sync.WaitGroup
	start := time.Now()
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repos := make([]model.Repository, 0, batchSize)
			for b := range batches {
				repos = repos[:0]
				for i := b[0]; i < b[1]; i++ {
					repos = append(repos, gen.Repo(i))
				}
				res, err := es.BulkIndex(ctx, index, repos)
				if err != nil {
					errOnce.Do(func() { firstErr = err })
					failed.Add(int64(len(repos)))
					continue
				}
				indexed.Add(int64(res.Indexed))
				failed.Add(int64(res.Failed))
				for _, e := range res.Errors {
					slog.Warn("document failed", "detail", e)
				}
			}
		}()
	}
	lastLog := time.Now()
	for lo := 0; lo < n; lo += batchSize {
		batches <- [2]int{lo, min(lo+batchSize, n)}
		if time.Since(lastLog) > 5*time.Second {
			done := indexed.Load()
			slog.Info("loading", "indexed", done, "of", n, "docs_per_sec", int(float64(done)/time.Since(start).Seconds()))
			lastLog = time.Now()
		}
	}
	close(batches)
	wg.Wait()
	load := time.Since(start)
	if firstErr != nil {
		return fmt.Errorf("bulk indexing failed: %w", firstErr)
	}

	refreshStart := time.Now()
	if err := es.UpdateSettings(ctx, index, map[string]any{"refresh_interval": "1s"}); err != nil {
		return err
	}
	if err := es.Refresh(ctx, index); err != nil {
		return err
	}
	refresh := time.Since(refreshStart)
	var merge time.Duration
	if o.forceMerge {
		slog.Info("force-merging to one segment per shard", "index", index)
		mergeStart := time.Now()
		if err := es.ForceMerge(ctx, index, 1); err != nil {
			return err
		}
		merge = time.Since(mergeStart)
	}
	if err := es.SwapAlias(ctx, index, old); err != nil {
		return err
	}
	if !o.keepOld {
		for _, idx := range old {
			_ = es.DeleteIndex(ctx, idx)
		}
	}
	stats, err := es.Stats(ctx, index)
	if err != nil {
		return err
	}

	rep := Report{
		Index: index, Alias: alias, Shards: o.shards, MergeSecs: merge.Seconds(), Docs: int(indexed.Load()), Failed: int(failed.Load()),
		Vectors: vectors, Readme: readme, BatchSize: batchSize, Workers: workers,
		LoadSeconds: load.Seconds(), DocsPerSec: float64(indexed.Load()) / load.Seconds(),
		RefreshSecs: refresh.Seconds(), IndexStats: stats, GeneratorSeed: seed,
	}
	if stats.Docs > 0 {
		rep.BytesPerDoc = float64(stats.StoreBytes) / float64(stats.Docs)
	}
	fmt.Printf("indexed %d docs (%d failed) into %s (%d shards) -> %s in %.1fs: %.0f docs/s; refresh %.1fs; merge %.1fs; %.1f MB, %d segments, %.0f bytes/doc\n",
		rep.Docs, rep.Failed, index, o.shards, alias, rep.LoadSeconds, rep.DocsPerSec, rep.RefreshSecs, rep.MergeSecs,
		float64(stats.StoreBytes)/1e6, stats.Segments, rep.BytesPerDoc)
	if o.jsonOut != "" {
		buf, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(o.jsonOut, append(buf, '\n'), 0o644); err != nil {
			return err
		}
	}
	if rep.Failed > 0 {
		return fmt.Errorf("%d documents failed to index", rep.Failed)
	}
	return nil
}
