// Package metrics exposes Prometheus instruments. Latency is measured from day one
// so later relevance / scaling experiments have a baseline to compare against.
package metrics

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "atlas_http_request_duration_seconds",
		Help:    "HTTP request latency by route and status.",
		Buckets: []float64{.005, .01, .025, .05, .1, .2, .5, 1, 2.5, 5},
	}, []string{"method", "route", "status"})

	SearchTook = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "atlas_search_took_seconds",
		Help:    "Elasticsearch-reported 'took' time for search requests.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .2, .5, 1},
	}, []string{"kind"})

	CacheResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "atlas_search_cache_total",
		Help: "Search cache lookups by result (hit / miss).",
	}, []string{"result"})

	PartialResults = promauto.NewCounter(prometheus.CounterOpts{
		Name: "atlas_search_partial_results_total",
		Help: "Searches answered with results from only some shards (a shard had no live copy, or timed out).",
	})
)

// Middleware records request latency per matched route.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		HTTPDuration.WithLabelValues(c.Request.Method, route, strconv.Itoa(c.Writer.Status())).
			Observe(time.Since(start).Seconds())
	}
}
