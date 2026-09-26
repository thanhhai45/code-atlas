# Experiment 04: Semantic and hybrid search

Roadmap Phase 9. Experiment 02 found that most recall misses are vocabulary mismatches. For example,
"messaging system" misses Kafka and Watermill because their descriptions use other words, and no lexical
trick fixed that without losing precision. This experiment adds dense embeddings and measures kNN-only and
hybrid (BM25 + kNN) retrieval against the lexical baseline.

## Hypothesis

1. Semantic retrieval recovers the vocabulary-mismatch misses (higher recall@10).
2. Hybrid retrieval keeps BM25's precision while adding that recall, so it beats lexical on NDCG@10.

## Setup

- **Model**: `sentence-transformers/all-MiniLM-L6-v2`, 384 dimensions, L2-normalized, run on CPU through ONNX
  Runtime (`fastembed`) in the new `ai-worker` (FastAPI). About 10 ms to embed one query.
  The model files come from fastembed's GCS mirror (`scripts/fetch_model.py`) because the development
  environment cannot reach huggingface.co.
- **Document text**: `name. description. Topics: … . Language: … . README[:1000]` (the model reads at most 256
  word pieces). Stored in PostgreSQL (`repositories.embedding REAL[]`) so reindexing never re-embeds.
- **Index**: `embedding` is a `dense_vector` (cosine, HNSW; Elasticsearch 8.15 defaults to `int8_hnsw`).
- **Queries**, all built by `search.BuildSearchQuery`, so evaluation runs exactly what `/search` runs:
  - `lexical`: the current BM25 + business-signal query
  - `semantic`: a query-level `knn` clause with the same filters
  - `hybrid`: `bool.should: [lexical, knn(boost)]`, i.e. linear score fusion. A document can match either way.
    The kNN clause sits *inside* the query, so hybrid works with sorting, `search_after` and `_rank_eval`
    (a top-level `knn` section also works, but the query-level form composes with everything else).
- **Why not RRF?** On the Basic license, `_rank_eval` rejects an `rrf` retriever with *"current license is
  non-compliant for Reciprocal Rank Fusion (RRF)"*. RRF is a paid feature, so it is neither shipped nor measured here.
- **Grid** (`rankeval -semantic-grid`): semantic with cosine threshold `minsim` ∈ {0, 0.3, 0.4, 0.5}, and hybrid
  with kNN boost ∈ {2, 5, 10, 20, 40} × `minsim` ∈ {0, 0.3, 0.4, 0.5}. The kNN score is (1 + cos) / 2 ∈ [0, 1],
  against BM25 scores of roughly 4–37.

## Guarding against pooling bias

The judgment list was written while search was lexical-only. Documents that only semantic retrieval surfaces
would be unrated, and `rankeval` counts unrated documents as irrelevant, which biases the comparison toward
lexical. The new `rankeval -pool` flag lists every unrated document in the top 10 of **any** variant.

All 267 of them were rated before comparing variants, on the same 0–3 scale and from the repositories'
descriptions and topics. The judgment list grew from 136 to 403 ratings, and after that no variant has an
unrated document in its top 10.

Rating the pool did not favour semantic retrieval. Nearly every pooled document was rated 0: without a
threshold, kNN returns the whole index. Hybrid's best NDCG@10 went slightly *down* (0.9827 → 0.9798) once
the pool was rated.

## Result (40 queries, fully judged)

| variant | ndcg@10 | mrr@10 | precision@5 | recall@10 |
|---|---|---|---|---|
| **lexical (default)** | **0.9830** | 1.0000 | **0.8750** | 0.8379 |
| semantic, minsim 0 | 0.9171 | 0.9583 | 0.3925 | 0.9524 |
| semantic, minsim 0.4 | 0.8589 | 0.8958 | 0.7342 | 0.7207 |
| hybrid, boost 2, minsim 0 | 0.9643 | 1.0000 | 0.4125 | **0.9825** |
| hybrid, boost 20, minsim 0.4 | 0.9718 | 1.0000 | 0.7783 | 0.8921 |
| **hybrid, boost 20, minsim 0.5** (best hybrid) | 0.9798 | 1.0000 | 0.8604 | 0.8650 |

Full grid: `docs/benchmarks/rankeval-semantic-grid.json`. The boost barely matters between 2 and 20. The cosine
threshold drives the recall/precision trade-off.

Queries that change between lexical and the best hybrid (NDCG@10 / recall@10):

| query | lexical | hybrid | what happened |
|---|---|---|---|
| `elasticsearch` | 0.931 / 0.75 | 0.985 / 1.00 | finds OpenSearch, whose description never says "elasticsearch" |
| `search-engine` | 0.936 / 1.00 | 0.996 / 1.00 | better ordering among engines |
| `rust-search-engine` | 0.934 / 1.00 | 1.000 / 1.00 | tantivy and meilisearch pulled above qdrant |
| `text-embeddings` | 1.000 / 0.33 | 1.000 / 0.67 | finds chroma ("embedding database") |
| `rag-framework-5k` | 1.000 / 0.75 | 0.951 / 1.00 | finds ragflow, but ranks it high |
| `elasticsearch-alternative` | **1.000** / 0.50 | **0.736** / 0.75 | ranks Elasticsearch itself first |

Latency through the API (50 documents, cache off, 40 requests each, everything on one development container):

| mode | p50 | p95 |
|---|---|---|
| lexical | 17 ms | 38 ms |
| hybrid | 35 ms | 62 ms |
| semantic | 19 ms | 30 ms |

Hybrid pays for both the query embedding (~10 ms) and the kNN clause on top of BM25.

## Explanation

- **Hypothesis 1 holds.** Semantic retrieval lifts recall@10 up to 0.98. It finds Kafka and Watermill for
  "messaging system" and OpenSearch for "elasticsearch", which no lexical change in experiment 02 managed.
- **Hypothesis 2 does not hold on this data.** Every configuration that adds meaningful recall also adds
  unrelated neighbours. A dense retriever always returns *something*, and on a 50-document index that
  something is mostly noise. That noise is what drops precision@5 from 0.875 to 0.41 at minsim 0. The best
  hybrid keeps precision close to lexical, but adds only +0.027 recall and gives back 0.003 NDCG.
- **Embeddings blur relations.** "elasticsearch alternative" and "elasticsearch" land close together in embedding
  space, so hybrid promotes the one repository the user explicitly does not want. Negations and relations such
  as "alternative to", "without" or "instead of" are a known weakness of bi-encoders.
- **The seed data flatters BM25.** Its descriptions were written by hand and contain the obvious keywords. Real
  GitHub descriptions are shorter and noisier, which is where vocabulary mismatch and dense retrieval matter
  most. The conclusion is about this dataset, not about hybrid search in general.

## Decision

- `DefaultMode` stays **lexical**, because it has the best NDCG@10 and precision@5 on the judgments.
- `mode=hybrid` (boost 20, minsim 0.5) and `mode=semantic` (no threshold, ranked purely by similarity) are
  available in the API and the UI. Both fall back to lexical, uncached, if the ai-worker is unavailable.
- **Similar repositories** now use kNN over the stored embedding, with `more_like_this` as the fallback for
  documents without one. There are no judgments for this feature, so the change is qualitative: for qdrant it
  returns chroma, milvus, weaviate, meilisearch, faiss and pgvector. A test checks that most of these are vector
  databases.

## Production implication

- Re-run `rankeval -semantic-grid -pool` after the first real crawl, and judge the pool before reading the
  numbers. With 10K+ real repositories, both the noise floor and the value of dense recall change.
- Candidates for the next experiment:
  - a stronger embedding model (bge-base / bge-small are on the same GCS mirror)
  - embedding the README separately from the description
  - a hybrid that only adds kNN candidates when BM25 returns few results
  - RRF, if a license that includes it becomes available
- `index.max_result_window` and `search_after` behave the same in hybrid mode (tested). kNN candidates are capped
  by `num_candidates` (100), so deep pages beyond that are lexical matches only.
