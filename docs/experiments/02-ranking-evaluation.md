# Experiment 02: Ranking evaluation with a judgment list and `_rank_eval`

> Historical record. The `default` ranking described here was later replaced; see
> [03-business-signal-tuning.md](03-business-signal-tuning.md) for the current weights and baseline.

Phase 5 of the roadmap: "benchmark and tune ranking" needs a ground truth first. This experiment
sets up that ground truth, records a baseline, and uses it to evaluate two candidate changes.

## Hypothesis

1. Without a judgment list, ranking changes are judged by eye and regressions go unnoticed.
2. The business signals in `function_score` (stars + recency) improve ranking over pure BM25.
3. Light English stemming improves recall (`embeddings` ↔ `embedding`, `logs` ↔ `log`).
4. Requiring fewer query terms to match (`minimum_should_match`) improves recall without hurting precision.

## Setup

- Dataset: `testdata/seed_repositories.json`, 50 repositories (hand-written sample data)
- Judgments: `testdata/judgments.json`, 30 queries, graded 0–3
  (0 irrelevant, 1 marginal, 2 relevant, 3 exactly what the user wants). The queries include the
  five example queries from the roadmap, navigational queries, a typo, a synonym, and queries with filters.
- Elasticsearch 8.15.3, single node, 1 shard, 0 replicas
- Tool: `cmd/rankeval`. It builds each request with `search.BuildRankEvalRequest`, which scores
  exactly like the live `/search` query, and evaluates two variants:
  - `default`: BM25 × (log(2 + stars) + 0.5 × recency decay)
  - `bm25_only`: the same text query without `function_score`

## Test

```bash
make up && make seed
make rankeval                        # = go run ./cmd/rankeval -v -min-ndcg 0.95 -min-recall 0.80
go run ./cmd/rankeval -json docs/benchmarks/rankeval-baseline.json
```

For a mapping change: edit `internal/search/index.json`, run `crawler -reindex` (new index + alias swap),
then run `rankeval` again.

## Metrics

| Metric | Meaning here |
|---|---|
| `ndcg@10` | Graded ranking quality. **Caveat:** Elasticsearch computes the ideal DCG over only as many documents as the query returned, so NDCG does **not** penalize relevant documents that were never retrieved (a query returning 1 perfect hit scores 1.0 even if 4 relevant repos are missing). |
| `recall@10` | Share of documents rated ≥ 2 found in the top 10. It covers the gap NDCG leaves, so the CI gate checks it too. |
| `mrr@10` | 1 / rank of the first document rated ≥ 2 |
| `precision@5` | Share of the top 5 rated ≥ 2. Unrated documents count as irrelevant (`ignore_unlabeled: false`), so unrated hits must be judged whenever `rankeval` reports them. |

## Result

### Baseline (`docs/benchmarks/rankeval-baseline.json`)

| variant | ndcg@10 | mrr@10 | precision@5 | recall@10 |
|---|---|---|---|---|
| default | 0.9824 | 0.9833 | 0.8956 | 0.8367 |
| bm25_only | 0.9828 | 1.0000 | 0.8956 | 0.8367 |

Queries where the two variants differ:

| query | default ndcg | bm25_only ndcg | why |
|---|---|---|---|
| `elasticsearch-typo` ("elastisearch") | 1.000 | 0.827 | Stars push the real Elasticsearch repo above clients that match the fuzzy term equally well. |
| `rust-search-engine` ("search engine" + Rust) | 0.748 | 0.934 | qdrant ("Vector Search Engine", rated 1) outranks tantivy and meilisearch (rated 3) thanks to stars. MRR drops to 0.5. |

Lowest recall (both variants):

| query | recall@10 | missed | cause |
|---|---|---|---|
| `messaging` ("messaging system") | 0.33 | kafka, watermill | Every query term must match (`2<75%`); these descriptions lack "system". |
| `code-search`, `text-embeddings`, `machine-learning`, `monitoring`, `run-llm-locally`, `elasticsearch-alternative` | 0.50 | e.g. chroma, loki, llama.cpp, meilisearch | Vocabulary gap: the relevant repos describe themselves with different words. |
| `similarity-search` | 0.60 | qdrant, weaviate | "vector search", not "similarity search" |

### Candidate A: light English stemming (`light_english` on description/README analyzers)

No change on any metric or any query. The token change works (`embeddings`, `embedding` → `embed`),
but every miss above has a second query term that is missing from the document, and stemming
cannot fix that. **Rejected:** no measured benefit, and stemming adds risk for repository names and
technical terms.

### Candidate B: `minimum_should_match: "-1"` on the recall clause (2-term queries need 1 term)

| variant | ndcg@10 | mrr@10 | precision@5 | recall@10 |
|---|---|---|---|---|
| default | 0.9381 (−0.044) | 0.9667 | 0.7267 (−0.169) | 0.8906 (+0.054) |

Recall improves (`messaging` 0.33 → 1.0, `text-embeddings` 0.5 → 1.0), but precision collapses:
"css framework" and "react framework" return every "framework", and "elasticsearch alternative"
ranks Elasticsearch itself first (NDCG 1.0 → 0.40). **Rejected.**

## Explanation

- On this dataset, **business signals are neutral on average**: they help navigational and typo
  queries, and hurt when a popular, partially relevant repository competes with the relevant ones.
  The next tuning step is to make the signal weaker (e.g. `factor` < 1 or `boost_mode: sum`) rather than remove it.
- **The main recall problem is vocabulary mismatch**, not morphology. Lexical tricks trade precision
  for recall one-for-one. This is the case for semantic / hybrid retrieval (Phase 9): add candidates
  that use different words, and keep the strict lexical clause for precision.
- 50 documents and 30 queries are enough to exercise the workflow, not to draw final conclusions.
  Re-run the baseline and extend the judgments after the first real crawl.

## Production implication

- Every ranking change now has a measurable, repeatable result. CI runs `rankeval` against a real
  Elasticsearch and fails below **NDCG@10 0.95** or **recall@10 0.80** (baseline 0.98 / 0.84).
  Raise the thresholds as the baseline improves.
- Gate on NDCG **and** recall: NDCG alone did not register Candidate B's recall gains, and it does
  not register lost results either.
- Treat `testdata/judgments.json` as code: extend it whenever `rankeval` reports unrated documents in
  the top k, and whenever the dataset changes. A test checks that it stays in sync with the seed data.
