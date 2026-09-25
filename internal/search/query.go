package search

import (
	"fmt"
	"strings"
)

const (
	DefaultPageSize = 20
	MaxPageSize     = 100
	// MaxResultWindow mirrors index.max_result_window. Deeper pages need search_after.
	MaxResultWindow = 10_000
)

// StarBucket is one row of the stars range facet.
type StarBucket struct {
	Key  string
	From *int
	To   *int
}

func intp(v int) *int { return &v }

var StarBuckets = []StarBucket{
	{Key: "<100", To: intp(100)},
	{Key: "100-1K", From: intp(100), To: intp(1000)},
	{Key: "1K-5K", From: intp(1000), To: intp(5000)},
	{Key: "5K-10K", From: intp(5000), To: intp(10000)},
	{Key: "10K+", From: intp(10000)},
}

// ActivityBuckets are relative to "now" and are rounded to the day so the
// query is cacheable by Elasticsearch's request cache.
var ActivityBuckets = []struct{ Key, From, To string }{
	{Key: "30d", From: "now-30d/d"},
	{Key: "90d", From: "now-90d/d"},
	{Key: "1y", From: "now-1y/d"},
	{Key: "older", To: "now-1y/d"},
}

// Params is a parsed /search request.
type Params struct {
	Query           string
	Languages       []string
	Licenses        []string
	Topics          []string
	MinStars        *int
	MaxStars        *int
	PushedWithin    string // e.g. "30d", "90d", "1y"; empty = any time
	IncludeArchived bool
	IncludeForks    bool
	Sort            string // relevance (default) | stars | updated
	Page            int    // 1-based
	Size            int
}

// Normalize clamps paging values and fixes defaults.
func (p *Params) Normalize() {
	p.Query = strings.TrimSpace(p.Query)
	if p.Size <= 0 {
		p.Size = DefaultPageSize
	}
	if p.Size > MaxPageSize {
		p.Size = MaxPageSize
	}
	if p.Page < 1 {
		p.Page = 1
	}
	if maxPage := MaxResultWindow / p.Size; p.Page > maxPage {
		p.Page = maxPage
	}
	switch p.Sort {
	case "stars", "updated", "relevance":
	default:
		p.Sort = "relevance"
	}
	switch p.PushedWithin {
	case "", "30d", "90d", "1y":
	default:
		p.PushedWithin = ""
	}
}

// facetFilters returns the user-selected facet filters keyed by facet name.
// They go into post_filter (not query) so each facet's counts can be computed
// with every *other* facet applied — the standard multi-select facet pattern.
func (p Params) facetFilters() map[string]map[string]any {
	f := map[string]map[string]any{}
	if len(p.Languages) > 0 {
		f["language"] = map[string]any{"terms": map[string]any{"language": p.Languages}}
	}
	if len(p.Licenses) > 0 {
		f["license"] = map[string]any{"terms": map[string]any{"license": p.Licenses}}
	}
	if len(p.Topics) > 0 {
		// A repository must carry every selected topic (AND), unlike language/license (OR).
		musts := []any{}
		for _, t := range p.Topics {
			musts = append(musts, map[string]any{"term": map[string]any{"topics": t}})
		}
		f["topics"] = map[string]any{"bool": map[string]any{"filter": musts}}
	}
	if p.MinStars != nil || p.MaxStars != nil {
		r := map[string]any{}
		if p.MinStars != nil {
			r["gte"] = *p.MinStars
		}
		if p.MaxStars != nil {
			r["lte"] = *p.MaxStars
		}
		f["stars"] = map[string]any{"range": map[string]any{"stars": r}}
	}
	if p.PushedWithin != "" {
		f["activity"] = map[string]any{"range": map[string]any{"pushed_at": map[string]any{"gte": "now-" + p.PushedWithin + "/d"}}}
	}
	return f
}

// textQuery separates recall from ranking:
//
//   - must: the query terms have to appear somewhere in all_text (a copy_to
//     catch-all of name, description, topics, language and README), so
//     "ruby elasticsearch" matches a repo whose language is Ruby and whose topic
//     is elasticsearch. A low-boost fuzzy variant (no synonyms) recovers typos
//     such as "elastisearch".
//   - should: best_fields with per-field boosts and a phrase match decide the
//     order among matching documents; they never add or remove results.
func textQuery(q string) map[string]any {
	return map[string]any{
		"bool": map[string]any{
			"must": []any{map[string]any{"bool": map[string]any{
				"should": []any{
					map[string]any{"match": map[string]any{"all_text": map[string]any{
						"query":                q,
						"minimum_should_match": "2<75%",
					}}},
					map[string]any{"match": map[string]any{"all_text": map[string]any{
						"query":                q,
						"fuzziness":            "AUTO",
						"prefix_length":        1,
						"analyzer":             "repo_text",
						"minimum_should_match": "2<75%",
						"boost":                0.3,
					}}},
				},
				"minimum_should_match": 1,
			}}},
			"should": []any{
				map[string]any{"multi_match": map[string]any{
					"query":       q,
					"type":        "best_fields",
					"fields":      []string{"name^5", "full_name^3", "topics^3", "description^2", "readme"},
					"tie_breaker": 0.3,
				}},
				map[string]any{"multi_match": map[string]any{
					"query":  q,
					"type":   "phrase",
					"fields": []string{"name^2", "description"},
					"boost":  2,
				}},
			},
		},
	}
}

// withBusinessSignals wraps a relevance query in function_score so popularity and
// recency influence ranking without drowning out text relevance:
//
//	final = bm25 * (log(2 + stars) + 0.5 * gauss(pushed_at))
//
// This is a starting point for Phase 5 tuning, not a final formula.
func withBusinessSignals(q map[string]any) map[string]any {
	return map[string]any{
		"function_score": map[string]any{
			"query": q,
			"functions": []any{
				map[string]any{"field_value_factor": map[string]any{
					"field": "stars", "modifier": "log2p", "factor": 1, "missing": 0,
				}},
				map[string]any{
					"gauss": map[string]any{"pushed_at": map[string]any{
						"origin": "now", "offset": "30d", "scale": "180d", "decay": 0.5,
					}},
					"weight": 0.5,
				},
			},
			"score_mode": "sum",
			"boost_mode": "multiply",
		},
	}
}

// RankingOptions toggles ranking components so their effect can be measured
// with the Ranking Evaluation API (see cmd/rankeval).
type RankingOptions struct {
	// DisableBusinessSignals scores by text relevance (BM25) only.
	DisableBusinessSignals bool
}

// buildQuery returns the "query" part of a search. extraFilters are added as
// non-scoring filters (used by rank evaluation, which does not support post_filter).
func buildQuery(p Params, opts RankingOptions, extraFilters []any) map[string]any {
	filters := []any{}
	if !p.IncludeArchived {
		filters = append(filters, map[string]any{"term": map[string]any{"archived": false}})
	}
	if !p.IncludeForks {
		filters = append(filters, map[string]any{"term": map[string]any{"fork": false}})
	}
	filters = append(filters, extraFilters...)

	boolQuery := map[string]any{"filter": filters}
	if p.Query != "" {
		boolQuery["must"] = []any{textQuery(p.Query)}
	}
	query := map[string]any{"bool": boolQuery}
	if p.Query != "" && p.Sort == "relevance" && !opts.DisableBusinessSignals {
		query = withBusinessSignals(query)
	}
	return query
}

// BuildRankEvalRequest returns the request used for one _rank_eval query. It
// scores exactly like BuildSearchQuery, but folds facet filters into the query
// (same hit set as post_filter) because _rank_eval only accepts a query.
func BuildRankEvalRequest(p Params, opts RankingOptions) map[string]any {
	p.Normalize()
	return map[string]any{"query": buildQuery(p, opts, filterValues(p.facetFilters(), ""))}
}

// BuildSearchQuery turns Params into an Elasticsearch _search body.
func BuildSearchQuery(p Params) map[string]any {
	p.Normalize()
	query := buildQuery(p, RankingOptions{}, nil)

	facets := p.facetFilters()
	body := map[string]any{
		"query":            query,
		"from":             (p.Page - 1) * p.Size,
		"size":             p.Size,
		"track_total_hits": true,
		"_source":          map[string]any{"excludes": []string{"readme"}},
		"sort":             sortClause(p),
		"aggs":             buildAggs(facets),
	}
	if len(facets) > 0 {
		body["post_filter"] = map[string]any{"bool": map[string]any{"filter": filterValues(facets, "")}}
	}
	if p.Query != "" {
		body["highlight"] = map[string]any{
			"encoder":   "html", // escape source text; only <mark> tags are trusted HTML
			"pre_tags":  []string{"<mark>"},
			"post_tags": []string{"</mark>"},
			"fields": map[string]any{
				"description": map[string]any{"number_of_fragments": 0},
				"readme":      map[string]any{"fragment_size": 160, "number_of_fragments": 2},
			},
		}
	}
	return body
}

func sortClause(p Params) []any {
	switch {
	case p.Sort == "stars" || (p.Sort == "relevance" && p.Query == ""):
		return []any{map[string]any{"stars": "desc"}, map[string]any{"id": "asc"}}
	case p.Sort == "updated":
		return []any{map[string]any{"pushed_at": "desc"}, map[string]any{"id": "asc"}}
	default:
		return []any{"_score", map[string]any{"stars": "desc"}, map[string]any{"id": "asc"}}
	}
}

// filterValues returns every facet filter except the one named exclude.
func filterValues(facets map[string]map[string]any, exclude string) []any {
	out := []any{}
	// Deterministic order keeps request bodies (and Redis cache keys) stable.
	for _, name := range []string{"language", "license", "topics", "stars", "activity"} {
		if f, ok := facets[name]; ok && name != exclude {
			out = append(out, f)
		}
	}
	return out
}

func buildAggs(facets map[string]map[string]any) map[string]any {
	starRanges := []any{}
	for _, b := range StarBuckets {
		r := map[string]any{"key": b.Key}
		if b.From != nil {
			r["from"] = *b.From
		}
		if b.To != nil {
			r["to"] = *b.To
		}
		starRanges = append(starRanges, r)
	}
	activityRanges := []any{}
	for _, b := range ActivityBuckets {
		r := map[string]any{"key": b.Key}
		if b.From != "" {
			r["from"] = b.From
		}
		if b.To != "" {
			r["to"] = b.To
		}
		activityRanges = append(activityRanges, r)
	}

	inner := map[string]map[string]any{
		"language": {"terms": map[string]any{"field": "language", "size": 20}},
		"license":  {"terms": map[string]any{"field": "license", "size": 20}},
		"topics":   {"terms": map[string]any{"field": "topics", "size": 30}},
		"stars":    {"range": map[string]any{"field": "stars", "ranges": starRanges}},
		"activity": {"date_range": map[string]any{"field": "pushed_at", "ranges": activityRanges}},
	}
	aggs := map[string]any{}
	for name, agg := range inner {
		aggs[name] = map[string]any{
			"filter": map[string]any{"bool": map[string]any{"filter": filterValues(facets, name)}},
			"aggs":   map[string]any{"values": agg},
		}
	}
	return aggs
}

// BuildSuggestQuery builds an as-you-type query over the search_as_you_type subfields.
func BuildSuggestQuery(prefix string, size int) map[string]any {
	return map[string]any{
		"size":    size,
		"_source": []string{"id", "full_name", "description", "stars", "language"},
		"query": map[string]any{
			"function_score": map[string]any{
				"query": map[string]any{"multi_match": map[string]any{
					"query": prefix,
					"type":  "bool_prefix",
					"fields": []string{
						"full_name.suggest", "full_name.suggest._2gram", "full_name.suggest._3gram",
					},
				}},
				"field_value_factor": map[string]any{"field": "stars", "modifier": "log2p", "missing": 0},
				"boost_mode":         "multiply",
			},
		},
	}
}

// BuildSimilarQuery finds repositories similar to id with more_like_this — a
// lexical baseline to compare against vector similarity in Phase 9.
func BuildSimilarQuery(index string, id int64, size int) map[string]any {
	return map[string]any{
		"size":    size,
		"_source": map[string]any{"excludes": []string{"readme"}},
		"query": map[string]any{
			"bool": map[string]any{
				"must": []any{map[string]any{"more_like_this": map[string]any{
					"fields":               []string{"description", "topics", "readme"},
					"like":                 []any{map[string]any{"_index": index, "_id": fmt.Sprint(id)}},
					"min_term_freq":        1,
					"min_doc_freq":         2,
					"max_query_terms":      30,
					"minimum_should_match": "20%",
				}}},
				"filter": []any{map[string]any{"term": map[string]any{"archived": false}}},
			},
		},
	}
}
