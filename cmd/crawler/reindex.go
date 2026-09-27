package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/thanhhai45/code-atlas/internal/embed"
	"github.com/thanhhai45/code-atlas/internal/model"
	"github.com/thanhhai45/code-atlas/internal/search"
	"github.com/thanhhai45/code-atlas/internal/store"
)

// reindex rebuilds the search index from PostgreSQL into a new versioned
// index and points the alias at it, without downtime and without making the
// live index worse. The alias moves only after the new index is complete,
// as redundant as the current one (replicas green) and not suspiciously
// smaller; any failure deletes the new index and leaves the alias alone.
//
//  1. Create the index with the current index's shard and replica counts
//     (or -shards / -replicas), load it with no replicas and no refreshes.
//  2. Refresh, optionally force-merge, then add the replicas: copying
//     finished segments is 1.5x faster than indexing every document twice
//     (experiment 07). Wait for green.
//  3. Check the document count, swap the alias atomically.
//  4. Catch up: the crawler kept writing to the old index while this ran, so
//     re-index every row written since the start into the new one.
func reindex(ctx context.Context, db *store.Store, es *search.Client, emb *embed.Client, o options) error {
	old, err := es.AliasTargets(ctx)
	if err != nil {
		return err
	}
	var current *search.IndexLayout
	var oldCount int64
	if len(old) == 1 {
		layout, err := es.Layout(ctx, old[0])
		if err != nil {
			return err
		}
		current = &layout
		if oldCount, err = es.Count(ctx, old[0]); err != nil {
			return err
		}
	}
	layout, err := planLayout(current, o.shards, o.replicas)
	if err != nil {
		return err
	}
	started, err := db.Now(ctx)
	if err != nil {
		return err
	}

	name := es.NewIndexName(time.Now())
	slog.Info("reindex: creating index", "index", name, "previous", old, "shards", layout.Shards, "replicas", layout.Replicas)
	if err := es.CreateIndexWithShards(ctx, name, layout.Shards); err != nil {
		return err
	}
	// From here on, any failure must not leave a half-built index behind.
	abort := func(err error) error {
		slog.Error("reindex aborted; alias unchanged", "err", err)
		_ = es.DeleteIndex(context.WithoutCancel(ctx), name)
		return err
	}
	if err := es.UpdateSettings(ctx, name, map[string]any{"refresh_interval": "-1", "number_of_replicas": 0}); err != nil {
		return abort(err)
	}

	total, backfilled := 0, 0
	load := func(batch []model.Repository) error {
		n, err := backfillEmbeddings(ctx, db, emb, batch)
		if err != nil {
			return err
		}
		backfilled += n
		res, err := es.BulkIndex(ctx, name, batch)
		if err != nil {
			return err
		}
		if res.Failed > 0 {
			return fmt.Errorf("%d documents failed to index, e.g. %v", res.Failed, res.Errors)
		}
		total += res.Indexed
		slog.Info("reindex: batch", "indexed", total, "embeddings_backfilled", backfilled)
		return nil
	}
	if err := db.StreamRepositories(ctx, o.batchSize, load); err != nil {
		return abort(err)
	}

	if err := es.UpdateSettings(ctx, name, map[string]any{"refresh_interval": "1s"}); err != nil {
		return abort(err)
	}
	if err := es.Refresh(ctx, name); err != nil {
		return abort(err)
	}
	if o.forceMerge {
		slog.Info("reindex: force-merging", "index", name)
		if err := es.ForceMerge(ctx, name, 1); err != nil {
			return abort(err)
		}
	}
	if layout.Replicas > 0 {
		slog.Info("reindex: adding replicas", "index", name, "replicas", layout.Replicas, "timeout", o.greenTimeout)
		if err := es.UpdateSettings(ctx, name, map[string]any{"number_of_replicas": layout.Replicas}); err != nil {
			return abort(err)
		}
		waitCtx, cancel := context.WithTimeout(ctx, o.greenTimeout)
		err := es.WaitForGreen(waitCtx, name)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				err = fmt.Errorf("replicas not allocated within %s (does the cluster have %d nodes?): %w", o.greenTimeout, layout.Replicas+1, err)
			}
			return abort(err)
		}
	}

	newCount, err := es.Count(ctx, name)
	if err != nil {
		return abort(err)
	}
	if err := checkCounts(int64(total), newCount, oldCount, o.maxShrink); err != nil {
		return abort(err)
	}

	if err := es.SwapAlias(ctx, name, old); err != nil {
		return abort(err)
	}
	slog.Info("reindex: alias swapped", "alias", es.Alias(), "index", name, "documents", newCount)

	// Rows the crawler wrote while the index was being built went to the old
	// index; from the swap on, writes go to the new one. Re-indexing every row
	// written since the start closes the gap (re-indexing a row is idempotent).
	caughtUp := 0
	err = db.StreamRepositoriesSyncedSince(ctx, started, o.batchSize, func(batch []model.Repository) error {
		res, err := es.BulkIndex(ctx, name, batch)
		caughtUp += res.Indexed
		if err == nil && res.Failed > 0 {
			err = fmt.Errorf("%d documents failed to index", res.Failed)
		}
		return err
	})
	if err != nil {
		// The new index is live and complete up to the start; only recent
		// writes may be missing, and the next crawl or reindex restores them.
		slog.Warn("reindex: catch-up of writes made during the reindex failed", "err", err)
	} else if caughtUp > 0 {
		slog.Info("reindex: caught up with writes made during the reindex", "documents", caughtUp)
	}

	if !o.keepOld {
		for _, idx := range old {
			if err := es.DeleteIndex(ctx, idx); err != nil {
				slog.Warn("could not delete old index", "index", idx, "err", err)
			}
		}
	}
	return nil
}

// planLayout picks the new index's shard and replica counts: the flags when
// set, otherwise the current index's, so that a reindex never quietly drops
// the redundancy an operator configured (e.g. replicas on a cluster).
func planLayout(current *search.IndexLayout, shards, replicas int) (search.IndexLayout, error) {
	layout := search.IndexLayout{Shards: 1, Replicas: 0}
	if current != nil {
		layout = *current
	}
	if shards > 0 {
		layout.Shards = shards
	}
	if replicas >= 0 {
		layout.Replicas = replicas
	}
	if layout.Shards < 1 {
		return layout, fmt.Errorf("invalid shard count %d", layout.Shards)
	}
	return layout, nil
}

// checkCounts refuses to swap to an index that is missing documents it was
// sent, or that is much smaller than the live index. PostgreSQL is the source
// of truth, but a sudden drop usually means the wrong database or a failed
// load, and swapping would hide most repositories from search.
func checkCounts(sent, indexed, live int64, maxShrink float64) error {
	if indexed != sent {
		return fmt.Errorf("new index has %d documents, %d were indexed", indexed, sent)
	}
	if live > 0 && float64(indexed) < float64(live)*(1-maxShrink) {
		return fmt.Errorf("new index has %d documents, %.0f%% fewer than the live index's %d (limit %.0f%%; raise -max-shrink if intended)",
			indexed, 100*(1-float64(indexed)/float64(live)), live, 100*maxShrink)
	}
	return nil
}

// backfillEmbeddings computes embeddings for rows stored before embeddings
// existed (or while the ai-worker was down) and persists them, so the next
// reindex is free. It returns how many it computed.
func backfillEmbeddings(ctx context.Context, db *store.Store, emb *embed.Client, batch []model.Repository) (int, error) {
	if emb == nil {
		return 0, nil
	}
	var missing []model.Repository
	var idx []int
	for i, r := range batch {
		if len(r.Embedding) == 0 {
			missing = append(missing, r)
			idx = append(idx, i)
		}
	}
	if len(missing) == 0 {
		return 0, nil
	}
	if err := embedAll(ctx, emb, missing); err != nil {
		return 0, fmt.Errorf("backfill embeddings: %w", err)
	}
	if err := db.UpdateEmbeddings(ctx, missing); err != nil {
		return 0, err
	}
	for j, i := range idx {
		batch[i].Embedding = missing[j].Embedding
	}
	return len(missing), nil
}
