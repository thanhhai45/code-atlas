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
	return repoJSONCreated(id, stars, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
}

func repoJSONCreated(id, stars int, created time.Time) string {
	return fmt.Sprintf(`{"id":%d,"name":"r%d","full_name":"o/r%d","owner":{"login":"o"},"html_url":"https://github.com/o/r%d",
		"stargazers_count":%d,"license":{"spdx_id":"MIT","name":"MIT License"},"topics":["x"],
		"created_at":%q,"updated_at":"2024-01-01T00:00:00Z","pushed_at":"2024-01-01T00:00:00Z"}`,
		id, id, id, id, stars, created.UTC().Format(time.RFC3339))
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

type fakeRepo struct {
	stars   int
	created time.Time
}

// withStars builds repositories with the given star counts (sorted desc) and
// creation times one hour apart.
func withStars(stars []int) []fakeRepo {
	out := make([]fakeRepo, len(stars))
	base := time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, s := range stars {
		out[i] = fakeRepo{stars: s, created: base.Add(time.Duration(i) * time.Hour)}
	}
	return out
}

// fakeSearch simulates the GitHub Search API over a fixed set of repositories
// (id = index + 1, sorted by stars desc): the stars:N / stars:A..B and
// created:A..B qualifiers, pagination, and the 1000-result cap per query.
func fakeSearch(t *testing.T, repos []fakeRepo) (*httptest.Server, *[]string) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 1 {
			queries = append(queries, q)
		}
		minStars, maxStars := 0, int(^uint(0)>>1)
		from, to := time.Time{}, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
		for _, tok := range strings.Fields(q) {
			switch {
			case strings.HasPrefix(tok, "stars:"):
				v := strings.TrimPrefix(tok, "stars:")
				lo, hi, isRange := strings.Cut(v, "..")
				minStars, _ = strconv.Atoi(lo)
				if !isRange {
					maxStars = minStars
				} else if hi != "*" {
					maxStars, _ = strconv.Atoi(hi)
				}
			case strings.HasPrefix(tok, "created:"):
				lo, hi, _ := strings.Cut(strings.TrimPrefix(tok, "created:"), "..")
				var err1, err2 error
				from, err1 = time.Parse(time.RFC3339, lo)
				to, err2 = time.Parse(time.RFC3339, hi)
				if err1 != nil || err2 != nil {
					t.Errorf("bad created qualifier %q", tok)
				}
			}
		}
		var match []string
		for i, repo := range repos {
			if repo.stars >= minStars && repo.stars <= maxStars && !repo.created.Before(from) && !repo.created.After(to) {
				match = append(match, repoJSONCreated(i+1, repo.stars, repo.created))
			}
		}
		total := len(match)
		if len(match) > MaxSearchPage*PerPage {
			match = match[:MaxSearchPage*PerPage]
		}
		start, end := min((page-1)*PerPage, len(match)), min(page*PerPage, len(match))
		fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, total, strings.Join(match[start:end], ","))
	}))
	return srv, &queries
}

// collect runs SearchAll and fails the test on duplicate deliveries.
func collect(t *testing.T, c *Client, opts SearchOptions) []model.Repository {
	t.Helper()
	var got []model.Repository
	seen := map[int64]bool{}
	n, err := c.SearchAll(context.Background(), opts, func(b []model.Repository) error {
		for _, r := range b {
			if seen[r.ID] {
				t.Fatalf("duplicate id %d delivered", r.ID)
			}
			seen[r.ID] = true
		}
		got = append(got, b...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != len(got) {
		t.Fatalf("SearchAll returned %d but delivered %d", n, len(got))
	}
	return got
}

func TestSearchAllMovesStarCursorPastThousandCap(t *testing.T) {
	stars := make([]int, 2500)
	for i := range stars {
		stars[i] = 100_000 - i*10 // unique, descending
	}
	srv, queries := fakeSearch(t, withStars(stars))
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
	srv, _ := fakeSearch(t, withStars(stars))
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

func TestSearchAllSlicesByCreatedWhenStarCursorIsStuck(t *testing.T) {
	// 300 repos with distinct stars, then 2500 sharing 42 stars (too many for one
	// query), then 200 below 42 that must still be reached afterwards.
	var stars []int
	for i := range 300 {
		stars = append(stars, 10_000-i)
	}
	for range 2500 {
		stars = append(stars, 42)
	}
	for i := range 200 {
		stars = append(stars, 41-i%30)
	}
	srv, queries := fakeSearch(t, withStars(stars))
	defer srv.Close()
	c, _ := newTestClient(srv)
	c.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

	got := collect(t, c, SearchOptions{MinStars: 1})
	if len(got) != len(stars) {
		t.Fatalf("got %d repositories, want all %d", len(got), len(stars))
	}
	sliced := 0
	for _, q := range *queries {
		if strings.Contains(q, "created:") {
			sliced++
			if !strings.Contains(q, "stars:42 ") {
				t.Errorf("created slicing must pin the stuck star count: %q", q)
			}
			if !strings.Contains(q, "+00:00..") {
				t.Errorf("created range must use the documented +00:00 offset form: %q", q)
			}
		}
	}
	if sliced == 0 {
		t.Error("expected creation-date slicing")
	}
	if last := (*queries)[len(*queries)-1]; !strings.HasSuffix(last, "stars:1..41") {
		t.Errorf("cursor should continue below the stuck count, last query %q", last)
	}
}

func TestSearchAllSlicingRespectsMax(t *testing.T) {
	stars := make([]int, 3000)
	for i := range stars {
		stars[i] = 7
	}
	srv, _ := fakeSearch(t, withStars(stars))
	defer srv.Close()
	c, _ := newTestClient(srv)
	c.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	if got := collect(t, c, SearchOptions{MinStars: 1, Max: 1700}); len(got) != 1700 {
		t.Fatalf("got %d, want 1700", len(got))
	}
}

func TestSliceByCreatedStopsAtMinimumWindow(t *testing.T) {
	// 1500 repositories created in the same second cannot be separated by date:
	// the crawler must take what one query returns and terminate.
	same := time.Date(2020, 5, 5, 5, 5, 5, 0, time.UTC)
	repos := make([]fakeRepo, 1500)
	for i := range repos {
		repos[i] = fakeRepo{stars: 3, created: same}
	}
	srv, queries := fakeSearch(t, repos)
	defer srv.Close()
	c, _ := newTestClient(srv)
	c.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

	got := collect(t, c, SearchOptions{MinStars: 3})
	if len(got) != MaxSearchPage*PerPage {
		t.Fatalf("got %d, want the capped %d", len(got), MaxSearchPage*PerPage)
	}
	// Bisecting ~18 years down to one second takes ~30 levels; anything far
	// beyond that means the recursion is not converging.
	if len(*queries) > 200 {
		t.Errorf("too many queries: %d", len(*queries))
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
