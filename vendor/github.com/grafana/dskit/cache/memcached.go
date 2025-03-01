package cache

import (
	"context"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// MemcachedCache is a memcached-based cache.
type MemcachedCache struct {
	logger    log.Logger
	memcached RemoteCacheClient
	name      string

	// Metrics.
	requests prometheus.Counter
	hits     prometheus.Counter
}

// NewMemcachedCache makes a new MemcachedCache.
func NewMemcachedCache(name string, logger log.Logger, memcached RemoteCacheClient, reg prometheus.Registerer) *MemcachedCache {
	c := &MemcachedCache{
		logger:    logger,
		memcached: memcached,
		name:      name,
	}

	c.requests = promauto.With(reg).NewCounter(prometheus.CounterOpts{
		Name:        "cache_memcached_requests_total",
		Help:        "Total number of items requests to memcached.",
		ConstLabels: prometheus.Labels{"name": name},
	})

	c.hits = promauto.With(reg).NewCounter(prometheus.CounterOpts{
		Name:        "cache_memcached_hits_total",
		Help:        "Total number of items requests to the cache that were a hit.",
		ConstLabels: prometheus.Labels{"name": name},
	})

	level.Info(logger).Log("msg", "created memcached cache")

	return c
}

// Store data identified by keys.
// The function enqueues the request and returns immediately: the entry will be
// asynchronously stored in the cache.
func (c *MemcachedCache) Store(ctx context.Context, data map[string][]byte, ttl time.Duration) {
	var (
		firstErr error
		failed   int
	)

	for key, val := range data {
		if err := c.memcached.SetAsync(ctx, key, val, ttl); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	if firstErr != nil {
		level.Warn(c.logger).Log("msg", "failed to store one or more items into memcached", "failed", failed, "firstErr", firstErr)
	}
}

// Fetch fetches multiple keys and returns a map containing cache hits, along with a list of missing keys.
// In case of error, it logs and return an empty cache hits map.
func (c *MemcachedCache) Fetch(ctx context.Context, keys []string) map[string][]byte {
	// Fetch the keys from memcached in a single request.
	c.requests.Add(float64(len(keys)))
	results := c.memcached.GetMulti(ctx, keys)
	c.hits.Add(float64(len(results)))
	return results
}

func (c *MemcachedCache) Name() string {
	return c.name
}
