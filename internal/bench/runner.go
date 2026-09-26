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
	"time"
)

// Runner executes workloads in a closed loop: each of Concurrency workers
// sends its next request as soon as the previous one returns. Latencies
// therefore exclude queueing that an open-loop (fixed-rate) client would see
// under overload ("coordinated omission"); compare runs at the same concurrency.
type Runner struct {
	BaseURL     string
	Concurrency int
	Duration    time.Duration
	Warmup      time.Duration
	Seed        uint64
	HTTP        *http.Client
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

func (rn Runner) do(ctx context.Context, req Request) sample {
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, strings.TrimRight(rn.BaseURL, "/")+req.Path, body)
	if err != nil {
		return sample{err: true}
	}
	if req.Body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	resp, err := rn.HTTP.Do(hr)
	if err != nil {
		return sample{err: true, latency: time.Since(start)}
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	lat := time.Since(start)
	if err != nil || resp.StatusCode >= 300 {
		return sample{err: true, latency: lat}
	}
	var res esResult
	if json.Unmarshal(data, &res) != nil || res.TimedOut || res.Shards.Failed > 0 {
		return sample{err: true, latency: lat}
	}
	hits := res.Hits.Total.Value
	if hits == 0 {
		hits = res.Total + int64(len(res.Items))
	}
	return sample{latency: lat, took: res.Took, hits: hits}
}

// Run warms up, then measures one workload for Duration.
func (rn Runner) Run(ctx context.Context, w Workload) Summary {
	if rn.Warmup > 0 {
		rn.loop(ctx, w, rn.Warmup, 1000)
	}
	samples, elapsed := rn.loop(ctx, w, rn.Duration, 0)
	return summarize(w.Name, rn.Concurrency, samples, elapsed)
}

func (rn Runner) loop(ctx context.Context, w Workload, d time.Duration, seedOffset uint64) ([]sample, time.Duration) {
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
				results[i] = append(results[i], rn.do(ctx, w.Build(r)))
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
