package search

import (
	"encoding/json"
	"strings"
	"testing"
)

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNormalize(t *testing.T) {
	p := Params{Query: "  go  ", Size: 1000, Page: 0, Sort: "bogus", PushedWithin: "5y"}
	p.Normalize()
	if p.Query != "go" || p.Size != MaxPageSize || p.Page != 1 || p.Sort != "relevance" || p.PushedWithin != "" {
		t.Fatalf("unexpected normalize result: %+v", p)
	}
	deep := Params{Size: 100, Page: 500}
	deep.Normalize()
	if deep.Page*deep.Size > MaxResultWindow {
		t.Fatalf("page %d x size %d exceeds result window", deep.Page, deep.Size)
	}
}

func TestBuildSearchQueryEmptyQuerySortsByStars(t *testing.T) {
	body := BuildSearchQuery(Params{})
	if _, ok := body["highlight"]; ok {
		t.Error("no highlight expected without a text query")
	}
	if _, ok := body["post_filter"]; ok {
		t.Error("no post_filter expected without facet filters")
	}
	q := toJSON(t, body["query"])
	if strings.Contains(q, "function_score") {
		t.Error("function_score should only wrap text queries")
	}
	if !strings.Contains(q, `{"term":{"archived":false}}`) || !strings.Contains(q, `{"term":{"fork":false}}`) {
		t.Errorf("archived/fork should be excluded by default: %s", q)
	}
	if got := toJSON(t, body["sort"]); !strings.HasPrefix(got, `[{"stars":"desc"}`) {
		t.Errorf("empty query should sort by stars, got %s", got)
	}
}

func TestBuildSearchQueryTextUsesRelevanceAndSignals(t *testing.T) {
	body := BuildSearchQuery(Params{Query: "vector database", Page: 2, Size: 10})
	if body["from"] != 10 || body["size"] != 10 {
		t.Errorf("paging: from=%v size=%v", body["from"], body["size"])
	}
	q := toJSON(t, body["query"])
	for _, want := range []string{"function_score", `"name^5"`, `"all_text"`, `"fuzziness":"AUTO"`, `"type":"phrase"`, `"field":"stars"`, `"pushed_at"`} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %s: %s", want, q)
		}
	}
	if got := toJSON(t, body["sort"]); !strings.HasPrefix(got, `["_score"`) {
		t.Errorf("text query should sort by _score, got %s", got)
	}
	if !strings.Contains(toJSON(t, body["highlight"]), `"encoder":"html"`) {
		t.Error("highlight must HTML-encode source text")
	}
}

func TestBuildSearchQueryExplicitSortSkipsFunctionScore(t *testing.T) {
	body := BuildSearchQuery(Params{Query: "orm", Sort: "updated"})
	if strings.Contains(toJSON(t, body["query"]), "function_score") {
		t.Error("function_score is pointless when sorting by a field")
	}
	if got := toJSON(t, body["sort"]); !strings.HasPrefix(got, `[{"pushed_at":"desc"}`) {
		t.Errorf("got sort %s", got)
	}
}

func TestFacetFiltersGoToPostFilterAndOtherAggs(t *testing.T) {
	min := 5000
	body := BuildSearchQuery(Params{
		Languages: []string{"Go", "Rust"}, Licenses: []string{"MIT"}, Topics: []string{"database", "search"},
		MinStars: &min, PushedWithin: "90d",
	})

	post := toJSON(t, body["post_filter"])
	for _, want := range []string{
		`{"terms":{"language":["Go","Rust"]}}`,
		`{"terms":{"license":["MIT"]}}`,
		`{"term":{"topics":"database"}}`,
		`{"term":{"topics":"search"}}`,
		`{"range":{"stars":{"gte":5000}}}`,
		`{"range":{"pushed_at":{"gte":"now-90d/d"}}}`,
	} {
		if !strings.Contains(post, want) {
			t.Errorf("post_filter missing %s: %s", want, post)
		}
	}
	// Facet filters must not narrow the main query, or facet counts would collapse.
	if strings.Contains(toJSON(t, body["query"]), "language") {
		t.Error("facet filters leaked into query")
	}

	aggs := body["aggs"].(map[string]any)
	langFilter := toJSON(t, aggs["language"].(map[string]any)["filter"])
	if strings.Contains(langFilter, `"language"`) {
		t.Errorf("language facet must ignore its own selection: %s", langFilter)
	}
	if !strings.Contains(langFilter, `"license"`) {
		t.Errorf("language facet must apply the license selection: %s", langFilter)
	}
	for _, name := range []string{"language", "license", "topics", "stars", "activity"} {
		if _, ok := aggs[name]; !ok {
			t.Errorf("missing aggregation %s", name)
		}
	}
}

func TestBuildSearchQueryIsDeterministic(t *testing.T) {
	p := Params{Query: "rag", Languages: []string{"Python"}, Licenses: []string{"MIT"}, Topics: []string{"llm"}}
	first := toJSON(t, BuildSearchQuery(p))
	for range 20 {
		if toJSON(t, BuildSearchQuery(p)) != first {
			t.Fatal("request body must be stable across calls")
		}
	}
}

func TestIndexDefinitionIsValidJSON(t *testing.T) {
	var def struct {
		Mappings struct {
			Properties map[string]any `json:"properties"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(IndexDefinition(), &def); err != nil {
		t.Fatal(err)
	}
	// Every field the query builder touches must exist in the mapping (dynamic: strict).
	for _, f := range []string{"name", "full_name", "description", "readme", "topics", "language", "license", "stars", "pushed_at", "archived", "fork", "id"} {
		if _, ok := def.Mappings.Properties[f]; !ok {
			t.Errorf("mapping missing field %s", f)
		}
	}
}
