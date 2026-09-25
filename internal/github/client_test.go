package github

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thanhhai45/code-atlas/internal/model"
)

func newTestClient(srv *httptest.Server) (*Client, *[]time.Duration) {
	c := NewClient(srv.URL, "token")
	c.SearchInterval = 0
	c.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	var slept []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	return c, &slept
}

func repoJSON(id, stars int) string {
	return fmt.Sprintf(`{"id":%d,"name":"r%d","full_name":"o/r%d","owner":{"login":"o"},"html_url":"https://github.com/o/r%d",
		"stargazers_count":%d,"license":{"spdx_id":"MIT","name":"MIT License"},"topics":["x"],
		"created_at":"2020-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z","pushed_at":"2024-01-01T00:00:00Z"}`, id, id, id, id, stars)
}

func TestNormalize(t *testing.T) {
	desc, lang := "  A fast DB  ", "Go"
	r := apiRepo{ID: 1, Name: "db", FullName: "o/db", Description: &desc, Language: &lang}
	r.License = &struct {
		SPDXID string `json:"spdx_id"`
		Name   string `json:"name"`
	}{SPDXID: "NOASSERTION", Name: "Other"}
	got := r.normalize()
	if got.Description != "A fast DB" || got.Language != "Go" || got.License != "" || got.Topics == nil {
		t.Fatalf("unexpected normalization: %+v", got)
	}
}

func TestBuildQuery(t *testing.T) {
	if got := buildQuery("language:go", 100, -1); got != "language:go stars:100..*" {
		t.Errorf("got %q", got)
	}
	if got := buildQuery("", 10, 500); got != "stars:10..500" {
		t.Errorf("got %q", got)
	}
}

func TestRetriesOnServerErrorAndRateLimit(t *testing.T) {
	var calls atomic.Int32
	reset := strconv.FormatInt(time.Now().Add(3*time.Second).Unix(), 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", reset)
			w.WriteHeader(http.StatusForbidden)
		case 3:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusForbidden)
		default:
			fmt.Fprintf(w, `{"total_count":1,"items":[%s]}`, repoJSON(1, 10))
		}
	}))
	defer srv.Close()
	c, slept := newTestClient(srv)

	repos, total, err := c.SearchPage(context.Background(), "stars:1..*", 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(repos) != 1 || repos[0].License != "MIT" {
		t.Fatalf("unexpected result: total=%d repos=%+v", total, repos)
	}
	if calls.Load() != 4 {
		t.Fatalf("expected 4 calls, got %d", calls.Load())
	}
	if len(*slept) != 3 {
		t.Fatalf("expected 3 sleeps, got %v", *slept)
	}
	if (*slept)[1] < time.Second || (*slept)[1] > 5*time.Second {
		t.Errorf("rate-limit sleep should wait for reset, got %v", (*slept)[1])
	}
	if (*slept)[2] != 7*time.Second {
		t.Errorf("secondary rate limit should honor Retry-After, got %v", (*slept)[2])
	}
}

func TestPermanentErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	c, _ := newTestClient(srv)
	if _, _, err := c.SearchPage(context.Background(), "bad", 1); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("422 must not be retried, got %d calls", calls.Load())
	}
}

// fakeSearch simulates GitHub search over a fixed star distribution, including
// the 1000-result cap per query.
func fakeSearch(t *testing.T, stars []int) (*httptest.Server, *[]string) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 1 {
			queries = append(queries, q)
		}
		rng := q[strings.Index(q, "stars:")+len("stars:"):]
		lo, hi, _ := strings.Cut(rng, "..")
		min, _ := strconv.Atoi(lo)
		max := int(^uint(0) >> 1)
		if hi != "*" {
			max, _ = strconv.Atoi(hi)
		}
		var match []string
		for i, s := range stars { // stars are sorted desc, id = index+1
			if s >= min && s <= max {
				match = append(match, repoJSON(i+1, s))
			}
		}
		total := len(match)
		if len(match) > MaxSearchPage*PerPage {
			match = match[:MaxSearchPage*PerPage]
		}
		start, end := (page-1)*PerPage, page*PerPage
		if start > len(match) {
			start = len(match)
		}
		if end > len(match) {
			end = len(match)
		}
		fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, total, strings.Join(match[start:end], ","))
	}))
	return srv, &queries
}

func TestSearchAllMovesStarCursorPastThousandCap(t *testing.T) {
	stars := make([]int, 2500)
	for i := range stars {
		stars[i] = 100_000 - i*10 // unique, descending
	}
	srv, queries := fakeSearch(t, stars)
	defer srv.Close()
	c, _ := newTestClient(srv)

	var got []model.Repository
	n, err := c.SearchAll(context.Background(), SearchOptions{MinStars: 1}, func(b []model.Repository) error {
		got = append(got, b...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2500 || len(got) != 2500 {
		t.Fatalf("expected all 2500 repositories, got n=%d len=%d", n, len(got))
	}
	ids := map[int64]bool{}
	for _, r := range got {
		if ids[r.ID] {
			t.Fatalf("duplicate id %d delivered", r.ID)
		}
		ids[r.ID] = true
	}
	if len(*queries) != 3 || !strings.HasSuffix((*queries)[1], fmt.Sprintf("..%d", stars[999])) {
		t.Errorf("unexpected query sequence: %v", *queries)
	}
}

func TestSearchAllRespectsMax(t *testing.T) {
	stars := make([]int, 500)
	for i := range stars {
		stars[i] = 1000 - i
	}
	srv, _ := fakeSearch(t, stars)
	defer srv.Close()
	c, _ := newTestClient(srv)
	var got int
	n, err := c.SearchAll(context.Background(), SearchOptions{MinStars: 1, Max: 150}, func(b []model.Repository) error {
		got += len(b)
		return nil
	})
	if err != nil || n != 150 || got != 150 {
		t.Fatalf("n=%d got=%d err=%v", n, got, err)
	}
}

func TestSearchAllStopsWhenCursorCannotAdvance(t *testing.T) {
	stars := make([]int, 1500)
	for i := range stars {
		stars[i] = 42 // every repository has the same star count
	}
	srv, _ := fakeSearch(t, stars)
	defer srv.Close()
	c, _ := newTestClient(srv)
	n, err := c.SearchAll(context.Background(), SearchOptions{MinStars: 1}, func([]model.Repository) error { return nil })
	if err != nil || n != 1000 {
		t.Fatalf("expected to stop at 1000 without looping, n=%d err=%v", n, err)
	}
}

func TestReadmeMissingIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c, _ := newTestClient(srv)
	got, err := c.Readme(context.Background(), "o/r")
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}
