package bench

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"

	"github.com/thanhhai45/code-atlas/internal/search"
	"github.com/thanhhai45/code-atlas/internal/synth"
)

// Request is one HTTP request of a workload.
type Request struct {
	Method string
	Path   string
	Body   []byte
}

// Workload builds randomized requests of one kind.
type Workload struct {
	Name        string
	Description string
	// NeedsVectors marks workloads that only make sense on an index with embeddings.
	NeedsVectors bool
	Build        func(r *rand.Rand) Request
}

// Env is what workloads draw their random inputs from. Queries use the
// generator's vocabulary, so they match documents the way real queries would.
type Env struct {
	Gen   *synth.Generator
	Docs  int    // documents in the index (for picking existing names)
	Alias string // Elasticsearch alias for the "es" target
}

func (e Env) topicWords(r *rand.Rand, n int) string {
	words := make([]string, 0, n)
	for range n {
		t := synth.Topics[zipf(r, len(synth.Topics))]
		words = append(words, strings.ReplaceAll(t, "-", " "))
	}
	return strings.Join(words, " ")
}

func (e Env) name(r *rand.Rand) string { return e.Gen.Name(r.IntN(max(1, e.Docs))) }

func zipf(r *rand.Rand, n int) int { return int(rand.NewZipf(r, 1.1, 1, uint64(n-1)).Uint64()) }

// typo drops one inner character, e.g. "database" -> "databse".
func typo(r *rand.Rand, w string) string {
	if len(w) < 5 {
		return w
	}
	i := 1 + r.IntN(len(w)-2)
	return w[:i] + w[i+1:]
}

func (e Env) esSearch(body map[string]any) Request {
	buf, _ := json.Marshal(body)
	return Request{Method: "POST", Path: "/" + url.PathEscape(e.Alias) + "/_search", Body: buf}
}

func withSize(body map[string]any, size int) map[string]any {
	body["size"] = size
	body["_source"] = map[string]any{"excludes": []string{"readme", "embedding"}}
	return body
}

func (e Env) filtered(r *rand.Rand) search.Params {
	langs, lics := synth.Languages(), synth.Licenses()
	minStars := []int{0, 10, 100, 1000}[r.IntN(4)]
	return search.Params{
		Query:     e.topicWords(r, 1+r.IntN(2)),
		Languages: []string{langs[r.IntN(len(langs))]},
		Licenses:  []string{lics[r.IntN(3)]}, // mostly MIT / Apache-2.0 / GPL-3.0, like real filters
		MinStars:  &minStars,
	}
}

// ESWorkloads covers the query types listed in the roadmap (Phase 10).
func ESWorkloads(e Env) []Workload {
	return []Workload{
		{Name: "exact", Description: "term query on full_name.keyword (an existing repository)", Build: func(r *rand.Rand) Request {
			return e.esSearch(withSize(map[string]any{"query": map[string]any{"term": map[string]any{"full_name.keyword": strings.ToLower(e.name(r))}}}, 10))
		}},
		{Name: "full_text", Description: "BM25 text query (the /search text clause), no signals, no aggregations", Build: func(r *rand.Rand) Request {
			p := search.Params{Query: e.topicWords(r, 1+r.IntN(3))}
			return e.esSearch(withSize(search.BuildRankEvalRequest(p, search.RankingOptions{DisableBusinessSignals: true}), 10))
		}},
		{Name: "bool_filter", Description: "text query + language, license and min-stars filters", Build: func(r *rand.Rand) Request {
			return e.esSearch(withSize(search.BuildRankEvalRequest(e.filtered(r), search.RankingOptions{DisableBusinessSignals: true}), 10))
		}},
		{Name: "function_score", Description: "text query with the default popularity + recency signals", Build: func(r *rand.Rand) Request {
			p := search.Params{Query: e.topicWords(r, 1+r.IntN(3))}
			return e.esSearch(withSize(search.BuildRankEvalRequest(p, search.RankingOptions{}), 10))
		}},
		{Name: "aggregations", Description: "all five facets over the whole index (size 0, match_all)", Build: func(r *rand.Rand) Request {
			body := search.BuildSearchQuery(search.Params{Sort: "stars"})
			body["size"] = 0
			delete(body, "sort")
			return e.esSearch(body)
		}},
		{Name: "full_search", Description: "the complete /search request: text + signals + facets + post_filter + highlight", Build: func(r *rand.Rand) Request {
			p := e.filtered(r)
			p.MinStars = nil
			return e.esSearch(search.BuildSearchQuery(p))
		}},
		// Diagnostic variants of full_search, each removing one component, to
		// attribute its cost (see docs/experiments/05-benchmark-baseline.md).
		{Name: "fs_no_aggs", Description: "full_search without facet aggregations", Build: func(r *rand.Rand) Request {
			p := e.filtered(r)
			p.MinStars = nil
			body := search.BuildSearchQuery(p)
			delete(body, "aggs")
			return e.esSearch(body)
		}},
		{Name: "fs_no_highlight", Description: "full_search without highlighting", Build: func(r *rand.Rand) Request {
			p := e.filtered(r)
			p.MinStars = nil
			body := search.BuildSearchQuery(p)
			delete(body, "highlight")
			return e.esSearch(body)
		}},
		{Name: "fs_track_10k", Description: "full_search counting hits only up to 10,000 (track_total_hits default)", Build: func(r *rand.Rand) Request {
			p := e.filtered(r)
			p.MinStars = nil
			body := search.BuildSearchQuery(p)
			delete(body, "track_total_hits")
			return e.esSearch(body)
		}},
		{Name: "fuzzy", Description: "fuzzy match (AUTO) on description with a one-character typo", Build: func(r *rand.Rand) Request {
			w := strings.Fields(e.topicWords(r, 1))[0]
			return e.esSearch(withSize(map[string]any{"query": map[string]any{"match": map[string]any{
				"description": map[string]any{"query": typo(r, w), "fuzziness": "AUTO"}}}}, 10))
		}},
		{Name: "wildcard", Description: "leading+trailing wildcard on full_name.keyword (*fragment*)", Build: func(r *rand.Rand) Request {
			n := strings.ToLower(e.name(r))
			start := r.IntN(max(1, len(n)-4))
			return e.esSearch(withSize(map[string]any{"query": map[string]any{"wildcard": map[string]any{
				"full_name.keyword": map[string]any{"value": "*" + n[start:start+min(4, len(n)-start)] + "*"}}}}, 10))
		}},
		{Name: "suggest", Description: "search_as_you_type autocomplete on a 3-6 character prefix", Build: func(r *rand.Rand) Request {
			n := e.name(r)
			name := n[strings.Index(n, "/")+1:]
			return e.esSearch(search.BuildSuggestQuery(name[:min(len(name), 3+r.IntN(4))], 8))
		}},
		{Name: "knn", Description: "query-level kNN on the embedding (num_candidates 100)", NeedsVectors: true, Build: func(r *rand.Rand) Request {
			v := e.Gen.QueryVector(r, zipf(r, len(synth.Topics)))
			return e.esSearch(withSize(map[string]any{"query": map[string]any{"knn": map[string]any{
				"field": "embedding", "query_vector": v, "num_candidates": 100}}}, 10))
		}},
		{Name: "hybrid", Description: "the default hybrid query: BM25 + signals + kNN (boost 20, cosine >= 0.5)", NeedsVectors: true, Build: func(r *rand.Rand) Request {
			t := zipf(r, len(synth.Topics))
			p := search.Params{Query: strings.ReplaceAll(synth.Topics[t], "-", " "), Mode: search.ModeHybrid, QueryVector: e.Gen.QueryVector(r, t)}
			return e.esSearch(withSize(search.BuildRankEvalRequest(p, search.RankingOptions{}), 10))
		}},
		{Name: "deep_from", Description: "sorted by stars, from+size page near the 10K window (from 9,000-9,980)", Build: func(r *rand.Rand) Request {
			body := search.BuildSearchQuery(search.Params{Sort: "stars", Size: 20, Page: 451 + r.IntN(49)})
			delete(body, "aggs")
			return e.esSearch(body)
		}},
		{Name: "search_after", Description: "sorted by stars, search_after from a random position (any depth)", Build: func(r *rand.Rand) Request {
			after := []any{r.IntN(50), synth.IDBase + r.IntN(max(1, e.Docs))}
			body := search.BuildSearchQuery(search.Params{Sort: "stars", Size: 20, SearchAfter: after})
			delete(body, "aggs")
			return e.esSearch(body)
		}},
	}
}

// APIWorkloads go through the Go API (Gin, Redis cache, Elasticsearch).
// Run the API with SEARCH_ALIAS set to the benchmark alias, and disable the
// cache (SEARCH_CACHE_TTL_SECONDS=0) unless measuring cache hits is the point.
func APIWorkloads(e Env) []Workload {
	return []Workload{
		{Name: "api_search", Description: "GET /search with a text query and filters", Build: func(r *rand.Rand) Request {
			p := e.filtered(r)
			v := url.Values{"q": {p.Query}, "language": p.Languages, "license": p.Licenses}
			return Request{Method: "GET", Path: "/search?" + v.Encode()}
		}},
		{Name: "api_suggest", Description: "GET /suggest with a 3-6 character prefix", Build: func(r *rand.Rand) Request {
			n := e.name(r)
			name := n[strings.Index(n, "/")+1:]
			return Request{Method: "GET", Path: "/suggest?q=" + url.QueryEscape(name[:min(len(name), 3+r.IntN(4))])}
		}},
	}
}

// Select filters workloads by comma-separated names ("all" keeps every one).
func Select(all []Workload, names string) ([]Workload, error) {
	if names == "" || names == "all" {
		return all, nil
	}
	byName := map[string]Workload{}
	for _, w := range all {
		byName[w.Name] = w
	}
	var out []Workload
	for _, n := range strings.Split(names, ",") {
		w, ok := byName[strings.TrimSpace(n)]
		if !ok {
			return nil, fmt.Errorf("unknown workload %q", n)
		}
		out = append(out, w)
	}
	return out, nil
}
