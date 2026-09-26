package bench

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thanhhai45/code-atlas/internal/synth"
)

func TestPercentile(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	for p, want := range map[float64]time.Duration{50: 50 * time.Millisecond, 95: 95 * time.Millisecond, 99: 99 * time.Millisecond, 100: 100 * time.Millisecond} {
		if got := Percentile(d, p); got != want {
			t.Errorf("p%v = %v, want %v", p, got, want)
		}
	}
	if Percentile(nil, 50) != 0 || Percentile(d[:1], 99) != time.Millisecond {
		t.Error("edge cases")
	}
}

func TestSummarizeCountsErrorsSeparately(t *testing.T) {
	s := summarize("w", 2, []sample{
		{latency: 10 * time.Millisecond, took: 4, hits: 3},
		{latency: 30 * time.Millisecond, took: 6, hits: 1},
		{latency: time.Second, err: true},
	}, 2*time.Second)
	if s.Requests != 3 || s.Errors != 1 || s.QPS != 1.5 || s.MaxMs != 30 || s.MeanTookMs != 5 || s.MeanHits != 2 {
		t.Fatalf("unexpected summary: %+v", s)
	}
}

func TestWorkloadsProduceValidRequests(t *testing.T) {
	env := Env{Gen: synth.New(1, 8), Docs: 1000, Alias: "bench"}
	r := rand.New(rand.NewPCG(1, 2))
	for _, w := range append(ESWorkloads(env), APIWorkloads(env)...) {
		for range 20 {
			req := w.Build(r)
			if req.Method == "POST" {
				var body map[string]any
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("%s: invalid JSON: %v", w.Name, err)
				}
				if !strings.HasPrefix(req.Path, "/bench/_search") {
					t.Fatalf("%s: path %s", w.Name, req.Path)
				}
			} else if !strings.HasPrefix(req.Path, "/search?") && !strings.HasPrefix(req.Path, "/suggest?") {
				t.Fatalf("%s: path %s", w.Name, req.Path)
			}
		}
	}
	if _, err := Select(ESWorkloads(env), "exact,knn"); err != nil {
		t.Error(err)
	}
	if _, err := Select(ESWorkloads(env), "nope"); err == nil {
		t.Error("unknown workloads must be rejected")
	}
}

func TestRunnerMeasuresAndCountsErrors(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1)%10 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"took":3,"timed_out":false,"_shards":{"failed":0},"hits":{"total":{"value":7}}}`))
	}))
	defer srv.Close()
	rn := &Runner{URLs: []string{srv.URL}, Concurrency: 4, Duration: 200 * time.Millisecond, HTTP: NewHTTPClient(4)}
	s := rn.Run(context.Background(), Workload{Name: "x", Build: func(*rand.Rand) Request {
		return Request{Method: "POST", Path: "/i/_search", Body: []byte(`{}`)}
	}})
	if s.Requests == 0 || s.Errors == 0 || s.ErrorRate > 0.2 || s.MeanHits != 7 || s.MeanTookMs != 3 {
		t.Fatalf("unexpected summary: %+v", s)
	}
}

func TestRunnerFailsOverToAnotherNode(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"took":1,"_shards":{"failed":0},"hits":{"total":{"value":1}}}`))
	}))
	defer ok.Close()
	partial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"took":1,"_shards":{"failed":1},"hits":{"total":{"value":1}}}`))
	}))
	defer partial.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close() // connection refused from now on

	build := func(*rand.Rand) Request { return Request{Method: "POST", Path: "/i/_search", Body: []byte(`{}`)} }
	rn := &Runner{URLs: []string{ok.URL, down.URL}, Concurrency: 2, Duration: 200 * time.Millisecond,
		HTTP: NewHTTPClient(2), Timeline: 100 * time.Millisecond}
	s := rn.Run(context.Background(), Workload{Name: "x", Build: build})
	if s.Errors != 0 || s.Retries == 0 || s.Retries > s.Requests {
		t.Fatalf("requests to the dead node must be retried on the live one: %+v", s)
	}
	if len(s.Timeline) < 2 || s.Timeline[0].Requests == 0 {
		t.Fatalf("expected a timeline: %+v", s.Timeline)
	}

	rn = &Runner{URLs: []string{partial.URL}, Concurrency: 1, Duration: 50 * time.Millisecond, HTTP: NewHTTPClient(1)}
	s = rn.Run(context.Background(), Workload{Name: "x", Build: build})
	if s.Errors == 0 || s.Partial != s.Errors {
		t.Fatalf("partial results must count as errors: %+v", s)
	}
}
