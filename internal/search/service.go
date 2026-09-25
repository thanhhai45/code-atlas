package search

import (
	"context"
	"encoding/json"
	"strconv"
)

// Hit is a single search result.
type Hit struct {
	ID          int64               `json:"id"`
	Score       *float64            `json:"score,omitempty"`
	FullName    string              `json:"full_name"`
	Name        string              `json:"name"`
	Owner       string              `json:"owner"`
	Description string              `json:"description"`
	URL         string              `json:"url"`
	Language    string              `json:"language,omitempty"`
	License     string              `json:"license,omitempty"`
	Topics      []string            `json:"topics"`
	Stars       int                 `json:"stars"`
	Forks       int                 `json:"forks"`
	Archived    bool                `json:"archived"`
	PushedAt    string              `json:"pushed_at"`
	Highlight   map[string][]string `json:"highlight,omitempty"`
}

type Bucket struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type Result struct {
	Total  int64               `json:"total"`
	Page   int                 `json:"page"`
	Size   int                 `json:"size"`
	TookMS int                 `json:"took_ms"`
	Hits   []Hit               `json:"hits"`
	Facets map[string][]Bucket `json:"facets"`
	// NextCursor continues after the last hit with search_after. It is set
	// whenever the page is full, including pages reached with from+size.
	NextCursor string `json:"next_cursor,omitempty"`
}

type esHit struct {
	ID        string              `json:"_id"`
	Score     *float64            `json:"_score"`
	Source    Hit                 `json:"_source"`
	Highlight map[string][]string `json:"highlight"`
	Sort      []json.Number       `json:"sort"`
}

type esResponse struct {
	Took int `json:"took"`
	Hits struct {
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
		Hits []esHit `json:"hits"`
	} `json:"hits"`
	Aggregations map[string]struct {
		Values struct {
			Buckets []struct {
				Key      any   `json:"key"`
				DocCount int64 `json:"doc_count"`
			} `json:"buckets"`
		} `json:"values"`
	} `json:"aggregations"`
}

func (r esResponse) hits() []Hit {
	out := make([]Hit, 0, len(r.Hits.Hits))
	for _, h := range r.Hits.Hits {
		hit := h.Source
		if id, err := strconv.ParseInt(h.ID, 10, 64); err == nil {
			hit.ID = id
		}
		hit.Score = h.Score
		hit.Highlight = h.Highlight
		if hit.Topics == nil {
			hit.Topics = []string{}
		}
		out = append(out, hit)
	}
	return out
}

// Search executes a faceted full-text search.
func (c *Client) Search(ctx context.Context, p Params) (Result, error) {
	p.Normalize()
	var res esResponse
	if err := c.RawSearch(ctx, BuildSearchQuery(p), &res); err != nil {
		return Result{}, err
	}
	out := Result{
		Total:  res.Hits.Total.Value,
		Page:   p.Page,
		Size:   p.Size,
		TookMS: res.Took,
		Hits:   res.hits(),
		Facets: map[string][]Bucket{},
	}
	if n := len(res.Hits.Hits); n > 0 && n == p.Size {
		last := res.Hits.Hits[n-1].Sort
		values := make([]any, len(last))
		for i, v := range last {
			values[i] = v
		}
		next, err := EncodeCursor(sortName(p), values)
		if err != nil {
			return Result{}, err
		}
		out.NextCursor = next
	}
	for name, agg := range res.Aggregations {
		buckets := []Bucket{}
		for _, b := range agg.Values.Buckets {
			key := ""
			switch k := b.Key.(type) {
			case string:
				key = k
			default:
				buf, _ := json.Marshal(k)
				key = string(buf)
			}
			buckets = append(buckets, Bucket{Key: key, Count: b.DocCount})
		}
		out.Facets[name] = buckets
	}
	return out, nil
}

// Suggest returns autocomplete candidates for a prefix.
func (c *Client) Suggest(ctx context.Context, prefix string, size int) ([]Hit, error) {
	var res esResponse
	if err := c.RawSearch(ctx, BuildSuggestQuery(prefix, size), &res); err != nil {
		return nil, err
	}
	return res.hits(), nil
}

// Similar returns repositories similar to the given one.
func (c *Client) Similar(ctx context.Context, id int64, size int) ([]Hit, error) {
	var res esResponse
	if err := c.RawSearch(ctx, BuildSimilarQuery(c.alias, id, size+1), &res); err != nil {
		return nil, err
	}
	hits := []Hit{}
	for _, h := range res.hits() {
		if h.ID != id && len(hits) < size {
			hits = append(hits, h)
		}
	}
	return hits, nil
}
