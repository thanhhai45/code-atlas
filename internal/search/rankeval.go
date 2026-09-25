package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// RatedDoc is a graded relevance judgment for one document.
// Scale: 0 irrelevant, 1 marginal, 2 relevant, 3 perfect.
type RatedDoc struct {
	ID     string
	Rating int
}

// EvalRequest is one query of a Ranking Evaluation API call.
type EvalRequest struct {
	ID      string
	Request map[string]any
	Ratings []RatedDoc
}

// EvalHit is a hit returned for an evaluated query; Rating is nil when unrated.
type EvalHit struct {
	ID     string
	Rating *int
}

type EvalQueryResult struct {
	Score       float64
	Hits        []EvalHit
	UnratedDocs []string
}

type EvalResult struct {
	Score    float64
	Queries  map[string]EvalQueryResult
	Failures map[string]string
}

// ResolveIDs maps full names (owner/name, case-insensitive) to document ids.
func (c *Client) ResolveIDs(ctx context.Context, fullNames []string) (map[string]string, error) {
	lower := make([]string, len(fullNames))
	for i, n := range fullNames {
		lower[i] = strings.ToLower(n)
	}
	body := map[string]any{
		"size":    len(fullNames),
		"_source": []string{"full_name"},
		"query":   map[string]any{"terms": map[string]any{"full_name.keyword": lower}},
	}
	var res struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source struct {
					FullName string `json:"full_name"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := c.RawSearch(ctx, body, &res); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, h := range res.Hits.Hits {
		out[strings.ToLower(h.Source.FullName)] = h.ID
	}
	return out, nil
}

// FullNames maps document ids to repository full names.
func (c *Client) FullNames(ctx context.Context, ids []string) (map[string]string, error) {
	body := map[string]any{
		"size":    len(ids),
		"_source": []string{"full_name"},
		"query":   map[string]any{"ids": map[string]any{"values": ids}},
	}
	var res struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source struct {
					FullName string `json:"full_name"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := c.RawSearch(ctx, body, &res); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, h := range res.Hits.Hits {
		out[h.ID] = h.Source.FullName
	}
	return out, nil
}

// RankEval runs the Ranking Evaluation API against index with a single metric,
// e.g. {"dcg": {"k": 10, "normalize": true}}.
//
// index must be the concrete index (not the alias): _rank_eval matches ratings
// against hits by _index + _id, and hits report the concrete index name.
func (c *Client) RankEval(ctx context.Context, index string, requests []EvalRequest, metric map[string]any) (EvalResult, error) {
	reqs := make([]any, 0, len(requests))
	for _, r := range requests {
		ratings := make([]any, 0, len(r.Ratings))
		for _, d := range r.Ratings {
			ratings = append(ratings, map[string]any{"_index": index, "_id": d.ID, "rating": d.Rating})
		}
		reqs = append(reqs, map[string]any{"id": r.ID, "request": r.Request, "ratings": ratings})
	}
	body := map[string]any{"requests": reqs, "metric": metric}

	var res struct {
		MetricScore float64 `json:"metric_score"`
		Details     map[string]struct {
			MetricScore float64 `json:"metric_score"`
			UnratedDocs []struct {
				ID string `json:"_id"`
			} `json:"unrated_docs"`
			Hits []struct {
				Hit struct {
					ID string `json:"_id"`
				} `json:"hit"`
				Rating *int `json:"rating"`
			} `json:"hits"`
		} `json:"details"`
		Failures map[string]json.RawMessage `json:"failures"`
	}
	path := "/" + url.PathEscape(index) + "/_rank_eval"
	if err := c.do(ctx, http.MethodPost, path, body, "", &res); err != nil {
		return EvalResult{}, err
	}
	out := EvalResult{Score: res.MetricScore, Queries: map[string]EvalQueryResult{}, Failures: map[string]string{}}
	for id, d := range res.Details {
		q := EvalQueryResult{Score: d.MetricScore}
		for _, h := range d.Hits {
			q.Hits = append(q.Hits, EvalHit{ID: h.Hit.ID, Rating: h.Rating})
		}
		for _, u := range d.UnratedDocs {
			q.UnratedDocs = append(q.UnratedDocs, u.ID)
		}
		out.Queries[id] = q
	}
	for id, f := range res.Failures {
		out.Failures[id] = truncate(string(f), 500)
	}
	return out, nil
}

// Metric helpers for the metrics used by cmd/rankeval.

func NDCG(k int) map[string]any {
	return map[string]any{"dcg": map[string]any{"k": k, "normalize": true}}
}

// MRR counts a document as relevant when its rating is >= 2.
func MRR(k int) map[string]any {
	return map[string]any{"mean_reciprocal_rank": map[string]any{"k": k, "relevant_rating_threshold": 2}}
}

// Precision counts unrated documents as irrelevant, which is why judgment
// lists should be extended whenever unrated documents show up in the top k.
func Precision(k int) map[string]any {
	return map[string]any{"precision": map[string]any{"k": k, "relevant_rating_threshold": 2, "ignore_unlabeled": false}}
}

func Recall(k int) map[string]any {
	return map[string]any{"recall": map[string]any{"k": k, "relevant_rating_threshold": 2}}
}

// MetricName is a short label such as "ndcg@10".
func MetricName(metric map[string]any) string {
	for name, v := range metric {
		k := ""
		if cfg, ok := v.(map[string]any); ok {
			if kv, ok := cfg["k"].(int); ok {
				k = "@" + strconv.Itoa(kv)
			}
		}
		switch name {
		case "dcg":
			name = "ndcg"
		case "mean_reciprocal_rank":
			name = "mrr"
		}
		return name + k
	}
	return ""
}
