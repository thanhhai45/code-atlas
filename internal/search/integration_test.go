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

	"github.com/thanhhai45/code-atlas/internal/embed"
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

// The tests below exercise the semantic path. They need the seed data indexed
// with embeddings (crawler run with EMBEDDINGS_URL) and the ai-worker for
// query vectors, and are skipped otherwise.

func embedQuery(t *testing.T, q string) []float32 {
	t.Helper()
	url := os.Getenv("EMBEDDINGS_URL")
	if url == "" {
		t.Skip("EMBEDDINGS_URL not set")
	}
	vectors, err := embed.NewClient(url).Embed(context.Background(), []string{q})
	if err != nil {
		t.Fatal(err)
	}
	return vectors[0]
}

func fullNames(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.FullName
	}
	return out
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// Semantic retrieval finds repositories that share no words with the query:
// "messaging system" does not lexically match Kafka's description.
func TestSemanticFindsVocabularyMismatch(t *testing.T) {
	c := newIntegrationClient(t)
	ctx := context.Background()
	vector := embedQuery(t, "messaging system")

	lexical, err := c.Search(ctx, Params{Query: "messaging system", Mode: ModeLexical, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if contains(fullNames(lexical.Hits), "apache/kafka") {
		t.Fatal("test premise broken: lexical search already finds kafka")
	}
	semantic, err := c.Search(ctx, Params{Query: "messaging system", Mode: ModeSemantic, QueryVector: vector, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if semantic.Mode != ModeSemantic {
		t.Fatalf("mode = %q", semantic.Mode)
	}
	if !contains(fullNames(semantic.Hits), "apache/kafka") {
		t.Errorf("semantic search should find kafka, got %v", fullNames(semantic.Hits))
	}
}

// Hybrid keeps every lexical match (they satisfy the should clause) and pages
// with cursors like any relevance search.
func TestHybridKeepsLexicalMatchesAndPages(t *testing.T) {
	c := newIntegrationClient(t)
	ctx := context.Background()
	q := "search engine"
	vector := embedQuery(t, q)

	lexical, err := c.Search(ctx, Params{Query: q, Mode: ModeLexical, Size: MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	hybrid, err := c.Search(ctx, Params{Query: q, Mode: ModeHybrid, QueryVector: vector, Size: MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, h := range hybrid.Hits {
		got[h.ID] = true
	}
	for _, h := range lexical.Hits {
		if !got[h.ID] {
			t.Errorf("hybrid dropped lexical match %s", h.FullName)
		}
	}

	var paged []int64
	p := Params{Query: q, Mode: ModeHybrid, QueryVector: vector, Size: 3}
	for range 50 {
		res, err := c.Search(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, ids(res.Hits)...)
		if res.NextCursor == "" {
			break
		}
		if p.SearchAfter, err = DecodeCursor(res.NextCursor, p); err != nil {
			t.Fatal(err)
		}
	}
	if want := ids(hybrid.Hits); len(paged) != len(want) {
		t.Fatalf("paged %d hybrid results, want %d", len(paged), len(want))
	}
}

func TestSimilarUsesEmbeddings(t *testing.T) {
	c := newIntegrationClient(t)
	embedQuery(t, "x") // skip unless the semantic setup is present
	const qdrant = 900000013
	hits, err := c.Similar(context.Background(), qdrant, 6)
	if err != nil {
		t.Fatal(err)
	}
	names := fullNames(hits)
	if len(hits) != 6 || contains(names, "qdrant/qdrant") {
		t.Fatalf("similar(qdrant) = %v", names)
	}
	vectorDBs := 0
	for _, n := range []string{"milvus-io/milvus", "weaviate/weaviate", "chroma-core/chroma", "pgvector/pgvector", "facebookresearch/faiss"} {
		if contains(names, n) {
			vectorDBs++
		}
	}
	if vectorDBs < 3 {
		t.Errorf("expected mostly vector databases, got %v", names)
	}
}
