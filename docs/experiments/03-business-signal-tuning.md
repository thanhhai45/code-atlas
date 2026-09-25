# Experiment 03: Tuning the business-signal weights

Follow-up to [02-ranking-evaluation.md](02-ranking-evaluation.md), which found that the stars + recency
signals were neutral on average. They helped a typo query and hurt "search engine" filtered to Rust,
where the popular, partially relevant qdrant outranked tantivy and meilisearch.

## Hypothesis

The signals are useful as tie-breakers but too strong as a multiplier. Weakening them, either by
dampening the multiplier or by adding them to the BM25 score instead of multiplying it, should keep
the typo fix and remove the Rust regression.

## Setup

- Same dataset and tooling as experiment 02: 50 seed repositories, Elasticsearch 8.15.3, `cmd/rankeval`
- The signal formula is now parameterized (`search.Signals`):

  ```text
  multiply: bm25 × (base + stars_w·log10(2 + stars) + rec_w·gauss(pushed_at))
  sum:      bm25 + (       stars_w·log10(2 + stars) + rec_w·gauss(pushed_at))
  ```

- The previous default was `multiply, base 0, stars_w 1, rec_w 0.5`.
- `rankeval -grid` evaluates 30 configurations: multiply with base ∈ {0, 1, 2, 5, 10, 20} × rec_w ∈ {0, 0.5, 1};
  sum with stars_w ∈ {0.5, 1, 2, 5} × rec_w ∈ {0, 1, 2}. The best configuration is picked by mean NDCG@10,
  with ties broken by MRR@10, then precision@5, then the weaker configuration.

## Test

```bash
make seed
go run ./cmd/rankeval -grid -json docs/benchmarks/rankeval-signal-grid.json
```

### Guarding against overfitting

The first grid ran on the 30 queries from experiment 02, and only **2** of them ranked differently across
the grid. It picked `multiply base=1 rec=0.5`. Choosing weights from two queries is overfitting, so before
deciding, 10 queries designed to probe popularity were added, with ratings written before re-running:

- navigational: `kubernetes`, `redis`, `grafana`
- broad, where popularity should break ties: `search engine`, `database`, `http router`
- a specific repository should beat a popular one: `go elasticsearch client`, `postgres vector search`,
  `trigram code search`, `llm inference`

With the 40 queries, the winner changed. The first pick was a fluke of too few discriminating queries.

## Result (40 queries)

Selected rows (full grid: `docs/benchmarks/rankeval-signal-grid.json`):

| configuration | ndcg@10 | mrr@10 | precision@5 | recall@10 |
|---|---|---|---|---|
| previous default (mul base=0 stars=1 rec=0.5) | 0.9804 | 0.9875 | 0.8750 | 0.8442 |
| bm25_only | 0.9822 | 1.0000 | 0.8750 | 0.8442 |
| mul base=1 stars=1 rec=0.5 (winner on 30 queries) | 0.9852 | 1.0000 | 0.8750 | 0.8442 |
| mul base=10 stars=1 rec=0.5 | 0.9822 | 1.0000 | 0.8750 | 0.8442 |
| **sum stars=0.5 rec=1 (new default)** | **0.9863** | **1.0000** | 0.8750 | 0.8442 |
| sum stars=1 rec=1 | 0.9844 | 1.0000 | 0.8750 | 0.8442 |
| sum stars=5 rec=1 | 0.9787 | 0.9875 | 0.8650 | 0.8442 |

Only four queries rank differently across the grid (NDCG@10):

| query | previous | bm25_only | mul base=1 rec=0.5 | **sum 0.5 / 1** |
|---|---|---|---|---|
| `rust-search-engine` | 0.748 | 0.934 | 0.934 | **0.934** |
| `database` | 0.915 | 0.973 | 0.915 | **0.973** |
| `elasticsearch-typo` | 1.000 | 0.827 | 1.000 | **0.988** |
| `search-engine` | 0.935 | 0.936 | 0.941 | **0.936** |

Recall never changes: the signals reorder results but do not add or remove them.

## Explanation

- **Multiply mode lets popularity override relevance.** log10(2 + stars) spans about 0.3 to 5.2 across
  the dataset, so with base 0 a very popular repository's score can be scaled up to ~17× more than a
  small one's. That is how qdrant (rated 1) beat tantivy (rated 3), and how popular but less relevant repos
  crowd the top of the broad `database` query. Dampening (base ≥ 5) removes the damage but also the benefit,
  converging to BM25-only.
- **Sum mode keeps the signals in the tie-breaker range.** With stars_w 0.5 and rec_w 1, the signals add at
  most ~3.6 points to BM25 scores that are mostly 4–37 on these queries (10th–90th percentile of
  the top-10 hits, median 16). Close text matches are ordered by
  popularity: the real Elasticsearch repository still wins the typo query. A clearly better text match is
  never overturned.
- **Stronger additive weights (stars_w 5) hurt**, which marks the upper bound of the useful range.
- The whole decision rests on 4 of 40 queries and a 0.006 NDCG difference over BM25-only. The robust
  conclusions are the direction (additive and small beats multiplicative) and the upper bound. The exact
  numbers are not robust.

## Production implication

- `search.DefaultSignals` is now `sum, stars_w 0.5, rec_w 1`. The CI gate and
  `docs/benchmarks/rankeval-baseline.json` use the new baseline: NDCG@10 0.9863, MRR@10 1.0,
  precision@5 0.875, recall@10 0.844.
- **Re-tune after the first real crawl.** In sum mode the signals compete with raw BM25 scores, and BM25
  grows with corpus size through IDF. On 100K repositories the same weights will act weaker. Re-run
  `rankeval -grid` on the real data with an extended judgment list.
- Add a query to the judgment list whenever a ranking complaint comes in. Queries that separate
  configurations are the scarce resource: 36 of these 40 queries rank identically under every configuration.
