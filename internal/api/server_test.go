package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/thanhhai45/code-atlas/internal/model"
	"github.com/thanhhai45/code-atlas/internal/search"
	"github.com/thanhhai45/code-atlas/internal/store"
)

type fakeRepos struct{ pingErr error }

func (f fakeRepos) Ping(context.Context) error { return f.pingErr }
func (f fakeRepos) GetRepository(_ context.Context, id int64) (model.Repository, error) {
	if id == 1 {
		return model.Repository{ID: 1, FullName: "o/r"}, nil
	}
	return model.Repository{}, store.ErrNotFound
}
func (f fakeRepos) ListRepositories(context.Context, int, int) ([]model.Repository, int, error) {
	return []model.Repository{{ID: 1}}, 1, nil
}

type fakeSearch struct {
	last  search.Params
	calls int
}

func (f *fakeSearch) Ping(context.Context) (string, error) { return "yellow", nil }
func (f *fakeSearch) Search(_ context.Context, p search.Params) (search.Result, error) {
	f.last, f.calls = p, f.calls+1
	return search.Result{Total: 1, Hits: []search.Hit{{ID: 1}}}, nil
}
func (f *fakeSearch) Suggest(context.Context, string, int) ([]search.Hit, error) {
	return []search.Hit{{ID: 1}}, nil
}
func (f *fakeSearch) Similar(context.Context, int64, int) ([]search.Hit, error) { return nil, nil }

type memCache struct{ m map[string][]byte }

func (c *memCache) Ping(context.Context) error { return errors.New("redis down") }
func (c *memCache) Get(_ context.Context, k string, out any) bool {
	b, ok := c.m[k]
	return ok && json.Unmarshal(b, out) == nil
}
func (c *memCache) Set(_ context.Context, k string, v any) { c.m[k], _ = json.Marshal(v) }

func do(t *testing.T, r http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func init() { gin.SetMode(gin.TestMode) }

func TestHealth(t *testing.T) {
	s := &Server{Repos: fakeRepos{}, Search: &fakeSearch{}, Cache: &memCache{m: map[string][]byte{}}}
	w := do(t, s.Router(), "/health")
	if w.Code != http.StatusOK {
		t.Fatalf("redis being down must not fail health: %d %s", w.Code, w.Body)
	}
	s.Repos = fakeRepos{pingErr: errors.New("boom")}
	if w := do(t, s.Router(), "/health"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when postgres is down, got %d", w.Code)
	}
}

func TestSearchParsesParamsAndCaches(t *testing.T) {
	fs := &fakeSearch{}
	s := &Server{Repos: fakeRepos{}, Search: fs, Cache: &memCache{m: map[string][]byte{}}}
	r := s.Router()
	path := "/search?q=vector+db&language=Go,Rust&language=Zig&license=MIT&topic=database&min_stars=5000&sort=stars&page=2&size=500&pushed_within=90d"

	w := do(t, r, path)
	if w.Code != http.StatusOK || w.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("first call: %d cache=%s", w.Code, w.Header().Get("X-Cache"))
	}
	p := fs.last
	if p.Query != "vector db" || len(p.Languages) != 3 || p.Languages[2] != "Zig" || p.Licenses[0] != "MIT" ||
		p.Topics[0] != "database" || p.MinStars == nil || *p.MinStars != 5000 || p.Sort != "stars" ||
		p.Page != 2 || p.Size != search.MaxPageSize || p.PushedWithin != "90d" {
		t.Fatalf("unexpected params: %+v", p)
	}

	w = do(t, r, path)
	if w.Header().Get("X-Cache") != "HIT" || fs.calls != 1 {
		t.Fatalf("second call should be served from cache (calls=%d)", fs.calls)
	}
}

func TestRepositoryEndpoints(t *testing.T) {
	s := &Server{Repos: fakeRepos{}, Search: &fakeSearch{}}
	r := s.Router()
	if w := do(t, r, "/repositories/1"); w.Code != http.StatusOK {
		t.Errorf("get: %d", w.Code)
	}
	if w := do(t, r, "/repositories/2"); w.Code != http.StatusNotFound {
		t.Errorf("missing: %d", w.Code)
	}
	if w := do(t, r, "/repositories/abc"); w.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", w.Code)
	}
	if w := do(t, r, "/repositories?page=1&size=10"); w.Code != http.StatusOK {
		t.Errorf("list: %d", w.Code)
	}
	if w := do(t, r, "/suggest?q=ela"); w.Code != http.StatusOK {
		t.Errorf("suggest: %d", w.Code)
	}
}

func TestSearchCursor(t *testing.T) {
	fs := &fakeSearch{}
	r := (&Server{Repos: fakeRepos{}, Search: fs}).Router()

	if w := do(t, r, "/search?q=orm&cursor=garbage"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor: got %d", w.Code)
	}
	if fs.calls != 0 {
		t.Fatal("an invalid cursor must not reach Elasticsearch")
	}

	cur, err := search.EncodeCursor("relevance:"+search.DefaultMode, []any{json.Number("2.5"), json.Number("40"), json.Number("9")})
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, r, "/search?q=orm&page=3&cursor="+cur); w.Code != http.StatusOK {
		t.Fatalf("valid cursor: got %d %s", w.Code, w.Body)
	}
	if len(fs.last.SearchAfter) != 3 || fs.last.Page != 1 {
		t.Errorf("cursor not applied: %+v", fs.last)
	}
	// A cursor issued for relevance order is rejected for stars order.
	if w := do(t, r, "/search?q=orm&sort=stars&cursor="+cur); w.Code != http.StatusBadRequest {
		t.Errorf("mismatched sort: got %d", w.Code)
	}
}

type fakeEmbedder struct {
	err   error
	calls int
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return [][]float32{{0.1, 0.2}}, nil
}
func (f *fakeEmbedder) Ping(context.Context) error { return f.err }

func TestSearchModes(t *testing.T) {
	fs, emb := &fakeSearch{}, &fakeEmbedder{}
	cache := &memCache{m: map[string][]byte{}}
	r := (&Server{Repos: fakeRepos{}, Search: fs, Cache: cache, Embedder: emb}).Router()

	do(t, r, "/search?q=orm&mode=hybrid")
	if emb.calls != 1 || len(fs.last.QueryVector) != 2 || fs.last.Mode != search.ModeHybrid {
		t.Fatalf("hybrid request not embedded: calls=%d params=%+v", emb.calls, fs.last)
	}
	do(t, r, "/search?q=orm&mode=lexical")
	do(t, r, "/search?q=orm&mode=hybrid&sort=stars")
	do(t, r, "/search?mode=hybrid")
	if emb.calls != 1 {
		t.Errorf("lexical, field-sorted and empty queries must not be embedded (calls=%d)", emb.calls)
	}
}

func TestSearchFallsBackToLexicalWhenEmbeddingFails(t *testing.T) {
	fs, emb := &fakeSearch{}, &fakeEmbedder{err: errors.New("worker down")}
	cache := &memCache{m: map[string][]byte{}}
	s := &Server{Repos: fakeRepos{}, Search: fs, Cache: cache, Embedder: emb}
	r := s.Router()

	if w := do(t, r, "/search?q=orm&mode=semantic"); w.Code != http.StatusOK {
		t.Fatalf("embedding failure must not fail the search: %d", w.Code)
	}
	if fs.last.QueryVector != nil {
		t.Error("no vector expected after a failed embedding")
	}
	if len(cache.m) != 0 {
		t.Error("degraded results must not be cached under the semantic key")
	}
	if w := do(t, r, "/health"); w.Code != http.StatusOK {
		t.Errorf("a down embedder degrades health, it does not fail it: %d", w.Code)
	}
}
