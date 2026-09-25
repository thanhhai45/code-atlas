// Package config loads runtime configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr         string
	DatabaseURL      string
	RedisURL         string
	ElasticsearchURL string
	// SearchAlias is the alias every read and write goes through. The concrete
	// index behind it is versioned (repositories_<timestamp>) so mappings can be
	// changed with a zero-downtime reindex + alias swap.
	SearchAlias    string
	SearchCacheTTL time.Duration
	GitHubToken    string
	GitHubAPIURL   string
}

func Load() Config {
	return Config{
		HTTPAddr:         env("HTTP_ADDR", ":8080"),
		DatabaseURL:      env("DATABASE_URL", "postgres://atlas:atlas@localhost:5432/atlas?sslmode=disable"),
		RedisURL:         env("REDIS_URL", "redis://localhost:6379/0"),
		ElasticsearchURL: env("ELASTICSEARCH_URL", "http://localhost:9200"),
		SearchAlias:      env("SEARCH_ALIAS", "repositories"),
		SearchCacheTTL:   time.Duration(envInt("SEARCH_CACHE_TTL_SECONDS", 60)) * time.Second,
		GitHubToken:      os.Getenv("GITHUB_TOKEN"),
		GitHubAPIURL:     env("GITHUB_API_URL", "https://api.github.com"),
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
