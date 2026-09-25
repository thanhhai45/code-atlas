package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRankEvalUsesConcreteIndexAndParsesDetails(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = io.WriteString(w, `{"metric_score":0.75,"details":{"q1":{"metric_score":0.75,
			"unrated_docs":[{"_index":"repositories_v2","_id":"9"}],
			"hits":[{"hit":{"_index":"repositories_v2","_id":"1"},"rating":3},{"hit":{"_index":"repositories_v2","_id":"9"},"rating":null}]}},
			"failures":{}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "repositories")
	res, err := c.RankEval(context.Background(), "repositories_v2", []EvalRequest{{
		ID: "q1", Request: map[string]any{"query": map[string]any{"match_all": map[string]any{}}},
		Ratings: []RatedDoc{{ID: "1", Rating: 3}},
	}}, NDCG(10))
	if err != nil {
		t.Fatal(err)
	}
	if path != "/repositories_v2/_rank_eval" {
		t.Errorf("path = %s", path)
	}
	rating := got["requests"].([]any)[0].(map[string]any)["ratings"].([]any)[0].(map[string]any)
	if rating["_index"] != "repositories_v2" || rating["_id"] != "1" {
		t.Errorf("ratings must reference the concrete index: %v", rating)
	}
	q := res.Queries["q1"]
	if res.Score != 0.75 || len(q.Hits) != 2 || q.Hits[0].Rating == nil || *q.Hits[0].Rating != 3 || q.Hits[1].Rating != nil {
		t.Fatalf("unexpected parse: %+v", res)
	}
	if len(q.UnratedDocs) != 1 || q.UnratedDocs[0] != "9" {
		t.Errorf("unrated docs: %v", q.UnratedDocs)
	}
}
