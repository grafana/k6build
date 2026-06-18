package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// catalogServer is a test catalog HTTP server whose contents and fetch count
// can be inspected and mutated concurrently.
type catalogServer struct {
	*httptest.Server
	mu      sync.Mutex
	entries map[string]entry
	fetches atomic.Int32
}

func newCatalogServer() *catalogServer {
	cs := &catalogServer{
		entries: map[string]entry{
			"k6":       {Module: "go.k6.io/k6", Versions: []string{"v0.1.0", "v0.2.0"}},
			"k6/x/ext": {Module: "go.k6.io/k6ext", Versions: []string{"v0.1.0"}},
		},
	}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cs.fetches.Add(1)
		cs.mu.Lock()
		data, _ := json.Marshal(cs.entries)
		cs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	return cs
}

func (cs *catalogServer) setVersions(name, module string, versions ...string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.entries[name] = entry{Module: module, Versions: versions}
}

func resolve(t *testing.T, c *CachedCatalog, name string) (Module, error) {
	t.Helper()
	return c.Resolve(context.Background(), Dependency{Name: name, Constrains: "*"})
}

func TestCachedCatalog_ServesFromCacheWhenSourceDown(t *testing.T) {
	t.Parallel()

	srv := newCatalogServer()

	cc, err := NewCachedCatalog(context.Background(), srv.URL, time.Hour, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Source goes down after the eager load on construction.
	srv.Close()

	mod, err := resolve(t, cc, "k6")
	if err != nil {
		t.Fatalf("expected cached catalog to serve, got: %v", err)
	}
	if mod.Version != "v0.2.0" {
		t.Fatalf("expected v0.2.0, got %s", mod.Version)
	}
}

func TestCachedCatalog_RefreshesAfterTTL(t *testing.T) {
	t.Parallel()

	srv := newCatalogServer()
	defer srv.Close()

	cc, err := NewCachedCatalog(context.Background(), srv.URL, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv.setVersions("k6", "go.k6.io/k6", "v0.1.0", "v0.2.0", "v0.3.0")

	// Within the TTL the new version is not visible yet.
	mod, _ := resolve(t, cc, "k6")
	if mod.Version != "v0.2.0" {
		t.Fatalf("expected stale v0.2.0 within TTL, got %s", mod.Version)
	}

	time.Sleep(20 * time.Millisecond)

	mod, err = resolve(t, cc, "k6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mod.Version != "v0.3.0" {
		t.Fatalf("expected v0.3.0 after TTL, got %s", mod.Version)
	}
}

func TestCachedCatalog_RefreshOnMiss(t *testing.T) {
	t.Parallel()

	srv := newCatalogServer()
	defer srv.Close()

	// Long TTL: the refresh must be triggered by the miss, not by expiry.
	cc, err := NewCachedCatalog(context.Background(), srv.URL, time.Hour, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err = resolve(t, cc, "k6/x/new"); !errors.Is(err, ErrUnknownDependency) {
		t.Fatalf("expected ErrUnknownDependency, got %v", err)
	}

	srv.setVersions("k6/x/new", "go.k6.io/new", "v0.1.0")

	mod, err := resolve(t, cc, "k6/x/new")
	if err != nil {
		t.Fatalf("expected refresh-on-miss to resolve, got: %v", err)
	}
	if mod.Version != "v0.1.0" {
		t.Fatalf("expected v0.1.0, got %s", mod.Version)
	}
}

func TestCachedCatalog_RefreshOnMissReturnsOriginalError(t *testing.T) {
	t.Parallel()

	srv := newCatalogServer()

	cc, err := NewCachedCatalog(context.Background(), srv.URL, time.Hour, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Source is down: the forced refresh fails, and the original resolution
	// error must be surfaced (not a download error).
	srv.Close()

	_, err = resolve(t, cc, "k6/x/unknown")
	if !errors.Is(err, ErrUnknownDependency) {
		t.Fatalf("expected ErrUnknownDependency, got %v", err)
	}
}

func TestCachedCatalog_CoalescesConcurrentRefreshes(t *testing.T) {
	t.Parallel()

	srv := newCatalogServer()
	defer srv.Close()

	cc, err := NewCachedCatalog(context.Background(), srv.URL, time.Hour, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv.fetches.Store(0) // ignore the eager load on construction

	// A burst of misses for an unknown dependency must trigger a single fetch.
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			_, _ = resolve(t, cc, "k6/x/unknown")
		}()
	}
	wg.Wait()

	if got := srv.fetches.Load(); got == 0 || got > goroutines/2 {
		t.Fatalf("expected refreshes to be coalesced into few fetches, got %d", got)
	}
}

func TestNewCachedCatalog_FailsOnInitialLoad(t *testing.T) {
	t.Parallel()

	srv := newCatalogServer()
	srv.Close() // source unreachable from the start

	_, err := NewCachedCatalog(context.Background(), srv.URL, time.Hour, nil)
	if err == nil {
		t.Fatal("expected error when the initial catalog load fails")
	}
}
