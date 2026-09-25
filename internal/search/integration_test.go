//go:build integration

// Integration tests against a real Elasticsearch loaded with
// testdata/seed_repositories.json (make seed, or the CI relevance job):
//
//	go test -tags integration ./internal/search/
package search

import (
	"context"
	"os"
	"testing"
)

func newIntegrationClient(t *testing.T) *Client {
	t.Helper()
	url := os.Getenv("ELASTICSEARCH_URL")
	if url == "" {
		url = "http://localhost:9200"
	}
	c := NewClient(url, "repositories")
	targets, err := c.AliasTargets(context.Background())
	if err != nil || len(targets) != 1 {
		t.Fatalf("expected a seeded index behind the alias: targets=%v err=%v", targets, err)
	}
	if err := c.Refresh(context.Background(), targets[0]); err != nil {
		t.Fatal(err)
	}
	return c
}

func ids(hits []Hit) []int64 {
	out := make([]int64, len(hits))
	for i, h := range hits {
		out[i] = h.ID
	}
	return out
}

// Paging with cursors must visit exactly the documents, in exactly the order,
// that one large page returns.
func TestSearchAfterMatchesSinglePage(t *testing.T) {
	c := newIntegrationClient(t)
	ctx := context.Background()
	cases := map[string]Params{
		"relevance":        {Query: "search"},
		"stars, no query":  {},
		"updated":          {Query: "database", Sort: "updated"},
		"stars + facets":   {Query: "framework", Sort: "stars", Licenses: []string{"MIT", "Apache-2.0"}},
		"relevance, typos": {Query: "elastisearch"},
	}
	for name, base := range cases {
		t.Run(name, func(t *testing.T) {
			all := base
			all.Size = MaxPageSize
			want, err := c.Search(ctx, all)
			if err != nil {
				t.Fatal(err)
			}
			if want.Total > MaxPageSize {
				t.Fatalf("test data too large for a single reference page: %d", want.Total)
			}

			var got []int64
			p := base
			p.Size = 3
			for pages := 0; ; pages++ {
				if pages > 50 {
					t.Fatal("cursor pagination did not terminate")
				}
				res, err := c.Search(ctx, p)
				if err != nil {
					t.Fatal(err)
				}
				if res.Total != want.Total {
					t.Errorf("page %d total = %d, want %d", pages, res.Total, want.Total)
				}
				got = append(got, ids(res.Hits)...)
				if res.NextCursor == "" {
					break
				}
				if p.SearchAfter, err = DecodeCursor(res.NextCursor, p); err != nil {
					t.Fatal(err)
				}
			}

			wantIDs := ids(want.Hits)
			if len(got) != len(wantIDs) {
				t.Fatalf("visited %d documents, want %d", len(got), len(wantIDs))
			}
			for i := range got {
				if got[i] != wantIDs[i] {
					t.Fatalf("position %d: got id %d, want %d", i, got[i], wantIDs[i])
				}
			}
		})
	}
}

// A cursor taken from an offset page continues exactly where page+1 would start,
// which is how the UI switches from page numbers to cursors at the result window.
func TestCursorFromOffsetPageContinuesAtNextPage(t *testing.T) {
	c := newIntegrationClient(t)
	ctx := context.Background()

	page2, err := c.Search(ctx, Params{Page: 2, Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	page3, err := c.Search(ctx, Params{Page: 3, Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	p := Params{Size: 5}
	p.Normalize()
	if p.SearchAfter, err = DecodeCursor(page2.NextCursor, p); err != nil {
		t.Fatal(err)
	}
	viaCursor, err := c.Search(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	a, b := ids(page3.Hits), ids(viaCursor.Hits)
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("page 3 %v vs cursor page %v", a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("page 3 %v vs cursor page %v", a, b)
		}
	}
}
