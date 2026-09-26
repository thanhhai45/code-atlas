// Package api exposes the HTTP API with Gin.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/thanhhai45/code-atlas/internal/cache"
	"github.com/thanhhai45/code-atlas/internal/metrics"
	"github.com/thanhhai45/code-atlas/internal/model"
	"github.com/thanhhai45/code-atlas/internal/search"
	"github.com/thanhhai45/code-atlas/internal/store"
)

// Repositories is the system-of-record dependency (PostgreSQL).
type Repositories interface {
	Ping(ctx context.Context) error
	GetRepository(ctx context.Context, id int64) (model.Repository, error)
	ListRepositories(ctx context.Context, limit, offset int) ([]model.Repository, int, error)
}

// Searcher is the search dependency (Elasticsearch).
type Searcher interface {
	Ping(ctx context.Context) (string, error)
	Search(ctx context.Context, p search.Params) (search.Result, error)
	Suggest(ctx context.Context, prefix string, size int) ([]search.Hit, error)
	Similar(ctx context.Context, id int64, size int) ([]search.Hit, error)
}

// Cache is optional; a nil Cache disables caching.
type Cache interface {
	Ping(ctx context.Context) error
	Get(ctx context.Context, key string, out any) bool
	Set(ctx context.Context, key string, v any)
}

type Server struct {
	Repos  Repositories
	Search Searcher
	Cache  Cache
}

func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), metrics.Middleware())

	r.GET("/health", s.health)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.GET("/repositories", s.listRepositories)
	r.GET("/repositories/:id", s.getRepository)
	r.GET("/repositories/:id/similar", s.similarRepositories)
	r.GET("/search", s.search)
	r.GET("/suggest", s.suggest)
	return r
}

func (s *Server) health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	checks := gin.H{}
	healthy := true
	if err := s.Repos.Ping(ctx); err != nil {
		checks["postgres"], healthy = "down: "+err.Error(), false
	} else {
		checks["postgres"] = "up"
	}
	if status, err := s.Search.Ping(ctx); err != nil || status == "red" {
		msg := "red"
		if err != nil {
			msg = err.Error()
		}
		checks["elasticsearch"], healthy = "down: "+msg, false
	} else {
		checks["elasticsearch"] = status
	}
	if s.Cache != nil {
		// Redis is a cache: degraded, not down.
		if err := s.Cache.Ping(ctx); err != nil {
			checks["redis"] = "degraded: " + err.Error()
		} else {
			checks["redis"] = "up"
		}
	}
	code, status := http.StatusOK, "ok"
	if !healthy {
		code, status = http.StatusServiceUnavailable, "unavailable"
	}
	c.JSON(code, gin.H{"status": status, "checks": checks})
}

func (s *Server) listRepositories(c *gin.Context) {
	page := queryInt(c, "page", 1, 1, 1_000_000)
	size := queryInt(c, "size", 20, 1, 100)
	repos, total, err := s.Repos.ListRepositories(c.Request.Context(), size, (page-1)*size)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "page": page, "size": size, "items": repos})
}

func (s *Server) getRepository(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	repo, err := s.Repos.GetRepository(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "repository not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, repo)
}

func (s *Server) similarRepositories(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	hits, err := s.Search.Similar(c.Request.Context(), id, queryInt(c, "size", 6, 1, 20))
	if errors.Is(err, search.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "repository not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": hits})
}

// ParseSearchParams maps query-string parameters to search.Params.
// Multi-value filters accept both repeated keys (?language=Go&language=Rust)
// and comma-separated values (?language=Go,Rust). A cursor (from a previous
// response's next_cursor) switches to search_after pagination.
func ParseSearchParams(c *gin.Context) (search.Params, error) {
	p := search.Params{
		Query:           c.Query("q"),
		Languages:       multi(c, "language"),
		Licenses:        multi(c, "license"),
		Topics:          multi(c, "topic"),
		PushedWithin:    c.Query("pushed_within"),
		IncludeArchived: c.Query("include_archived") == "true",
		IncludeForks:    c.Query("include_forks") == "true",
		Sort:            c.Query("sort"),
		Page:            queryInt(c, "page", 1, 1, search.MaxResultWindow),
		Size:            queryInt(c, "size", search.DefaultPageSize, 1, search.MaxPageSize),
	}
	if v, err := strconv.Atoi(c.Query("min_stars")); err == nil && v >= 0 {
		p.MinStars = &v
	}
	if v, err := strconv.Atoi(c.Query("max_stars")); err == nil && v >= 0 {
		p.MaxStars = &v
	}
	p.Normalize()
	if cur := c.Query("cursor"); cur != "" {
		values, err := search.DecodeCursor(cur, p)
		if err != nil {
			return p, err
		}
		p.SearchAfter = values
		p.Normalize()
	}
	return p, nil
}

func (s *Server) search(c *gin.Context) {
	p, err := ParseSearchParams(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cursor: it is malformed or belongs to a different sort order"})
		return
	}
	ctx := c.Request.Context()
	key := cache.Key("search", p)

	var res search.Result
	if s.Cache != nil && s.Cache.Get(ctx, key, &res) {
		metrics.CacheResults.WithLabelValues("hit").Inc()
		c.Header("X-Cache", "HIT")
		c.JSON(http.StatusOK, res)
		return
	}
	metrics.CacheResults.WithLabelValues("miss").Inc()

	res, err = s.Search.Search(ctx, p)
	if err != nil {
		internalError(c, err)
		return
	}
	metrics.SearchTook.WithLabelValues("search").Observe(float64(res.TookMS) / 1000)
	if s.Cache != nil {
		s.Cache.Set(ctx, key, res)
	}
	c.Header("X-Cache", "MISS")
	c.JSON(http.StatusOK, res)
}

func (s *Server) suggest(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		c.JSON(http.StatusOK, gin.H{"items": []search.Hit{}})
		return
	}
	if len(q) > 100 {
		q = q[:100]
	}
	hits, err := s.Search.Suggest(c.Request.Context(), q, queryInt(c, "size", 8, 1, 20))
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": hits})
}

func multi(c *gin.Context, key string) []string {
	var out []string
	for _, v := range c.QueryArray(key) {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func queryInt(c *gin.Context, key string, def, lo, hi int) int {
	v, err := strconv.Atoi(c.Query(key))
	if err != nil {
		return def
	}
	return max(lo, min(hi, v))
}

func pathID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a positive integer"})
		return 0, false
	}
	return id, true
}

func internalError(c *gin.Context, err error) {
	slog.Error("request failed", "method", c.Request.Method, "path", c.Request.URL.Path, "err", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
}
