package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// CachedCatalogConfig defines the configuration for a CachedCatalog
type CachedCatalogConfig struct {
	// Location is the catalog source (file path or URL)
	Location string
	// TTL is the time-to-live for the cached catalog.
	// After this period, the catalog is refreshed on next access.
	// A value of 0 means always refresh (preserving pre-cache behavior).
	TTL time.Duration
	// Log is an optional logger. If nil, slog.Default() is used.
	Log *slog.Logger
}

// CachedCatalog wraps a Catalog with TTL-based caching and refresh-on-miss.
//
// On construction, it fetches the catalog eagerly (fail-fast).
// Within the TTL window, all Resolve calls use the cached catalog.
// If a Resolve returns ErrUnknownDependency or ErrCannotSatisfy, a forced
// refresh is attempted before returning the error.
type CachedCatalog struct {
	location string
	ttl      time.Duration
	log      *slog.Logger

	mu        sync.RWMutex
	catalog   Catalog
	updatedAt time.Time

	refreshMu sync.Mutex
}

// NewCachedCatalog creates a CachedCatalog that eagerly loads the catalog
// from the given location. Returns an error if the initial fetch fails.
func NewCachedCatalog(ctx context.Context, cfg CachedCatalogConfig) (*CachedCatalog, error) {
	if cfg.Location == "" {
		return nil, fmt.Errorf("catalog location cannot be empty")
	}

	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	ctlg, err := NewCatalog(ctx, cfg.Location)
	if err != nil {
		return nil, fmt.Errorf("loading initial catalog: %w", err)
	}

	return &CachedCatalog{
		location:  cfg.Location,
		ttl:       cfg.TTL,
		log:       log,
		catalog:   ctlg,
		updatedAt: time.Now(),
	}, nil
}

// Resolve returns a Module that satisfies a Dependency.
// It serves from the cached catalog when possible, refreshing on TTL expiry
// or when a dependency cannot be resolved.
func (c *CachedCatalog) Resolve(ctx context.Context, dep Dependency) (Module, error) {
	// If TTL expired, attempt a non-blocking refresh
	if c.isExpired() {
		if c.refreshMu.TryLock() {
			c.doRefresh(ctx)
			c.refreshMu.Unlock()
		}
	}

	mod, err := c.getCatalog().Resolve(ctx, dep)
	if err != nil && (errors.Is(err, ErrUnknownDependency) || errors.Is(err, ErrCannotSatisfy)) {
		return c.refreshAndRetry(ctx, dep, err)
	}

	return mod, err
}

// isExpired returns true if the cached catalog has exceeded its TTL.
func (c *CachedCatalog) isExpired() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.ttl == 0 {
		return true
	}

	return time.Since(c.updatedAt) >= c.ttl
}

// getCatalog returns the current cached catalog.
func (c *CachedCatalog) getCatalog() Catalog {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.catalog
}

// doRefresh fetches a fresh catalog and swaps it in. Caller must hold refreshMu.
// If the fetch fails, the stale catalog is kept and a warning is logged.
func (c *CachedCatalog) doRefresh(ctx context.Context) {
	// Double-check: skip if another goroutine already refreshed within TTL
	c.mu.RLock()
	if c.ttl > 0 && time.Since(c.updatedAt) < c.ttl {
		c.mu.RUnlock()
		return
	}
	c.mu.RUnlock()

	fresh, err := NewCatalog(ctx, c.location)
	if err != nil {
		c.log.Warn("failed to refresh catalog, using cached data", "error", err)
		return
	}

	c.mu.Lock()
	c.catalog = fresh
	c.updatedAt = time.Now()
	c.mu.Unlock()

	c.log.Info("catalog refreshed")
}

// refreshAndRetry forces a catalog refresh and retries the resolution.
// If the refresh fails, originalErr is returned (not the download error).
func (c *CachedCatalog) refreshAndRetry(ctx context.Context, dep Dependency, originalErr error) (Module, error) {
	// Record the timestamp before acquiring the lock so we can detect
	// if another goroutine refreshed while we were waiting.
	c.mu.RLock()
	prevUpdatedAt := c.updatedAt
	c.mu.RUnlock()

	c.refreshMu.Lock()

	// Check if someone already refreshed while we were waiting for the lock
	c.mu.RLock()
	alreadyRefreshed := c.updatedAt.After(prevUpdatedAt)
	c.mu.RUnlock()

	if !alreadyRefreshed {
		fresh, err := NewCatalog(ctx, c.location)
		if err != nil {
			c.refreshMu.Unlock()
			c.log.Warn("failed to refresh catalog on resolution miss", "dependency", dep.Name, "error", err)
			return Module{}, originalErr
		}

		c.mu.Lock()
		c.catalog = fresh
		c.updatedAt = time.Now()
		c.mu.Unlock()

		c.log.Info("catalog refreshed on resolution miss", "dependency", dep.Name)
	}

	c.refreshMu.Unlock()

	// Retry with the (possibly updated) catalog
	mod, err := c.getCatalog().Resolve(ctx, dep)
	if err != nil {
		return Module{}, err
	}

	return mod, nil
}
