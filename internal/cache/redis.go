// Package cache is a best-effort Redis cache: a cache failure must never fail a request.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

type Cache struct {
	rdb *redis.Client
	ttl time.Duration
}

func New(url string, ttl time.Duration) (*Cache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	return &Cache{rdb: redis.NewClient(opts), ttl: ttl}, nil
}

func (c *Cache) Close() error { return c.rdb.Close() }

func (c *Cache) Ping(ctx context.Context) error { return c.rdb.Ping(ctx).Err() }

// Key derives a compact cache key from a namespace and any JSON-serializable value.
func Key(namespace string, v any) string {
	buf, _ := json.Marshal(v)
	sum := sha256.Sum256(buf)
	return "atlas:" + namespace + ":" + hex.EncodeToString(sum[:16])
}

// Get decodes a cached value into out and reports whether it was found.
func (c *Cache) Get(ctx context.Context, key string, out any) bool {
	if c == nil || c.ttl <= 0 {
		return false
	}
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return false
	}
	return json.Unmarshal(data, out) == nil
}

func (c *Cache) Set(ctx context.Context, key string, v any) {
	if c == nil || c.ttl <= 0 {
		return
	}
	if data, err := json.Marshal(v); err == nil {
		c.rdb.Set(ctx, key, data, c.ttl)
	}
}
