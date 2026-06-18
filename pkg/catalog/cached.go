package catalog

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// CachedCatalog wraps a catalog location with TTL-based caching and
// refresh-on-miss.
//
// The catalog is fetched eagerly on construction and then served from memory.
// Once the TTL expires, the next Resolve refreshes the catalog before serving.
// If a dependency cannot be resolved from the cached catalog, a refresh is
// forced regardless of the TTL and the resolution retried once, so newly
// published dependencies are picked up without waiting for the TTL to elapse.
//
// When a refresh fails (e.g. the catalog source is unreachable) the previously
// cached catalog keeps being served, so the service stays available even while
// the source is down.
//
// Concurrent refreshes are coalesced into a single fetch, so a burst of expired
// or unresolved requests triggers at most one download.
type CachedCatalog struct {
	location string
	ttl      time.Duration
	log      *slog.Logger

	mu        sync.Mutex
	catalog   Catalog
	updatedAt time.Time

	group singleflight.Group
}

// NewCachedCatalog creates a CachedCatalog, eagerly loading the catalog from
// location. It fails if the initial load fails.
//
// A ttl of 0 disables caching: every Resolve refreshes the catalog first
// (preserving the pre-cache behaviour). If log is nil, slog.Default() is used.
func NewCachedCatalog(
	ctx context.Context,
	location string,
	ttl time.Duration,
	log *slog.Logger,
) (*CachedCatalog, error) {
	if log == nil {
		log = slog.Default()
	}

	ctlg, err := NewCatalog(ctx, location)
	if err != nil {
		return nil, err
	}

	return &CachedCatalog{
		location:  location,
		ttl:       ttl,
		log:       log,
		catalog:   ctlg,
		updatedAt: time.Now(),
	}, nil
}

// Resolve returns a Module that satisfies dep, serving from the cached catalog
// and refreshing as described in the type documentation.
func (c *CachedCatalog) Resolve(ctx context.Context, dep Dependency) (Module, error) {
	mod, err := c.cached(ctx).Resolve(ctx, dep)
	if err == nil {
		return mod, nil
	}

	// Only a catalog miss is worth a forced refresh; other errors (e.g. an
	// invalid constraint) won't be fixed by a newer catalog.
	if !errors.Is(err, ErrUnknownDependency) && !errors.Is(err, ErrCannotSatisfy) {
		return Module{}, err
	}

	ctlg, refreshErr := c.refresh(ctx)
	if refreshErr != nil {
		// Keep serving the cached catalog and surface the original resolution
		// error rather than the refresh failure.
		c.log.Warn("could not refresh catalog on unresolved dependency",
			"dependency", dep.Name, "location", c.location, "error", refreshErr)
		return Module{}, err
	}

	return ctlg.Resolve(ctx, dep)
}

// cached returns the cached catalog, refreshing it first if the TTL has
// expired. A failed refresh keeps serving the stale catalog.
func (c *CachedCatalog) cached(ctx context.Context) Catalog {
	if !c.expired() {
		return c.current()
	}

	if _, err := c.refresh(ctx); err != nil {
		c.log.Warn("could not refresh expired catalog, serving stale data",
			"location", c.location, "error", err)
	}

	return c.current()
}

// expired reports whether the cached catalog has exceeded its TTL.
func (c *CachedCatalog) expired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.updatedAt) >= c.ttl
}

// current returns the currently cached catalog.
func (c *CachedCatalog) current() Catalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.catalog
}

// refresh fetches a fresh catalog and swaps it in, returning the new catalog.
// Concurrent calls are coalesced through singleflight so only one fetch runs at
// a time; the in-flight fetch's result is shared with all waiting callers.
func (c *CachedCatalog) refresh(ctx context.Context) (Catalog, error) {
	_, err, _ := c.group.Do("refresh", func() (any, error) {
		fresh, err := NewCatalog(ctx, c.location)
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		c.catalog = fresh
		c.updatedAt = time.Now()
		c.mu.Unlock()

		c.log.Info("catalog refreshed", "location", c.location)

		return fresh, nil
	})
	if err != nil {
		return nil, err
	}

	// Read back the current catalog rather than the singleflight result, so all
	// coalesced callers observe the same swapped-in value without a type
	// assertion on the shared any.
	return c.current(), nil
}
