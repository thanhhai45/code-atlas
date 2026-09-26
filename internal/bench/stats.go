// Package bench runs search workloads against Elasticsearch or the API and
// reports latency percentiles, throughput and error rate.
package bench

import (
	"math"
	"sort"
	"time"
)

// Summary aggregates the samples of one workload.
type Summary struct {
	Workload    string  `json:"workload"`
	Concurrency int     `json:"concurrency"`
	Requests    int     `json:"requests"`
	Errors      int     `json:"errors"`
	ErrorRate   float64 `json:"error_rate"`
	QPS         float64 `json:"qps"`
	P50ms       float64 `json:"p50_ms"`
	P95ms       float64 `json:"p95_ms"`
	P99ms       float64 `json:"p99_ms"`
	MaxMs       float64 `json:"max_ms"`
	MeanMs      float64 `json:"mean_ms"`
	// MeanTookMs is Elasticsearch's own "took" (time spent inside the cluster),
	// so MeanMs - MeanTookMs approximates network + (de)serialization cost.
	MeanTookMs float64 `json:"mean_took_ms"`
	// MeanHits is the average number of matching documents, to catch workloads
	// that accidentally match nothing (fast but meaningless).
	MeanHits float64 `json:"mean_hits"`
}

// Percentile returns the nearest-rank percentile (0 < p <= 100) of sorted values.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	rank = max(1, min(rank, len(sorted)))
	return sorted[rank-1]
}

type sample struct {
	latency time.Duration
	took    int
	hits    int64
	err     bool
}

func summarize(workload string, concurrency int, samples []sample, elapsed time.Duration) Summary {
	s := Summary{Workload: workload, Concurrency: concurrency, Requests: len(samples)}
	if len(samples) == 0 {
		return s
	}
	lat := make([]time.Duration, 0, len(samples))
	var total time.Duration
	var took, hits float64
	ok := 0
	for _, x := range samples {
		if x.err {
			s.Errors++
			continue
		}
		ok++
		lat = append(lat, x.latency)
		total += x.latency
		took += float64(x.took)
		hits += float64(x.hits)
	}
	s.ErrorRate = float64(s.Errors) / float64(len(samples))
	s.QPS = float64(len(samples)) / elapsed.Seconds()
	if ok == 0 {
		return s
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	s.P50ms, s.P95ms, s.P99ms = ms(Percentile(lat, 50)), ms(Percentile(lat, 95)), ms(Percentile(lat, 99))
	s.MaxMs = ms(lat[len(lat)-1])
	s.MeanMs = ms(total / time.Duration(ok))
	s.MeanTookMs = took / float64(ok)
	s.MeanHits = hits / float64(ok)
	return s
}
