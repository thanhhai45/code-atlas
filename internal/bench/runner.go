package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Runner executes workloads in a closed loop: each of Concurrency workers
// sends its next request as soon as the previous one returns. Latencies
// therefore exclude queueing that an open-loop (fixed-rate) client would see
// under overload ("coordinated omission"); compare runs at the same concurrency.
//
// With several URLs (the nodes of a cluster), requests go round-robin across
// them, and a request that cannot reach a node (connection refused or reset)
// is retried once on the next node, as Elasticsearch clients do. A node
// failure then shows up as retries, not as errors, unless the cluster itself
// cannot answer.
type Runner struct {
	URLs        []string
	Concurrency int
	Duration    time.Duration
	Warmup      time.Duration
	Seed        uint64
	HTTP        *http.Client
	// Timeline, when positive, also reports requests, errors and P95 per
	// interval of this length (to watch a run through a node failure).
	Timeline time.Duration

	next atomic.Uint64
}

func NewHTTPClient(concurrency int) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		// Enough idle connections for every worker, so the benchmark does not
		// measure TCP connection setup.
		Transport: &http.Transport{MaxIdleConns: concurrency * 2, MaxIdleConnsPerHost: concurrency * 2, IdleConnTimeout: 90 * time.Second},
	}
}

type esResult struct {
	Took     int  `json:"took"`
	TimedOut bool `json:"timed_out"`
	Shards   struct {
		Failed int `json:"failed"`
	} `json:"_shards"`
	Hits struct {
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
	} `json:"hits"`
	// API /search responses
	Total int64 `json:"total"`
	Items []any `json:"items"`
}

func (rn *Runner) send(ctx context.Context, base string, req Request) (*http.Response, error) {
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, strings.TrimRight(base, "/")+req.Path, body)
	if err != nil {
		return nil, err
	}
	if req.Body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	return rn.HTTP.Do(hr)
}

func (rn *Runner) do(ctx context.Context, req Request) sample {
	n := rn.next.Add(1)
	start := time.Now()
	resp, err := rn.send(ctx, rn.URLs[n%uint64(len(rn.URLs))], req)
	retried := false
	if err != nil && len(rn.URLs) > 1 && ctx.Err() == nil {
		retried = true
		resp, err = rn.send(ctx, rn.URLs[(n+1)%uint64(len(rn.URLs))], req)
	}
	if err != nil {
		return sample{err: true, retried: retried, latency: time.Since(start)}
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	lat := time.Since(start)
	if err != nil || resp.StatusCode >= 300 {
		return sample{err: true, retried: retried, latency: lat}
	}
	var res esResult
	if json.Unmarshal(data, &res) != nil || res.TimedOut {
		return sample{err: true, retried: retried, latency: lat}
	}
	if res.Shards.Failed > 0 {
		// HTTP 200 with results from only some shards: an answer, but a wrong one.
		return sample{err: true, partial: true, retried: retried, latency: lat}
	}
	hits := res.Hits.Total.Value
	if hits == 0 {
		hits = res.Total + int64(len(res.Items))
	}
	return sample{latency: lat, took: res.Took, hits: hits, retried: retried}
}

// Run warms up, then measures one workload for Duration.
func (rn *Runner) Run(ctx context.Context, w Workload) Summary {
	if rn.Warmup > 0 {
		rn.loop(ctx, w, rn.Warmup, 1000)
	}
	samples, elapsed := rn.loop(ctx, w, rn.Duration, 0)
	s := summarize(w.Name, rn.Concurrency, samples, elapsed)
	if rn.Timeline > 0 {
		s.Timeline = timeline(samples, rn.Timeline)
	}
	return s
}

func (rn *Runner) loop(ctx context.Context, w Workload, d time.Duration, seedOffset uint64) ([]sample, time.Duration) {
	deadline := time.Now().Add(d)
	results := make([][]sample, rn.Concurrency)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range rn.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(rn.Seed+seedOffset, uint64(i)))
			for time.Now().Before(deadline) && ctx.Err() == nil {
				x := rn.do(ctx, w.Build(r))
				x.at = time.Since(start)
				results[i] = append(results[i], x)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	var all []sample
	for _, rs := range results {
		all = append(all, rs...)
	}
	return all, elapsed
}
