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

func testCatalogJSON(entries map[string]entry) []byte {
	data, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return data
}

func baseCatalog() map[string]entry {
	return map[string]entry{
		"k6": {
			Module:   "go.k6.io/k6",
			Versions: []string{"v0.1.0", "v0.2.0"},
		},
		"k6/x/ext": {
			Module:   "go.k6.io/k6ext",
			Versions: []string{"v0.1.0", "v0.2.0"},
		},
	}
}

func TestCachedCatalog_ServesFromCache(t *testing.T) {
	t.Parallel()

	entries := baseCatalog()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(testCatalogJSON(entries)) //nolint:errcheck
	}))
	defer srv.Close()

	cc, err := NewCachedCatalog(context.Background(), CachedCatalogConfig{
		Location: srv.URL,
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Resolve while server is up
	mod, err := cc.Resolve(context.Background(), Dependency{Name: "k6", Constrains: "*"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mod.Version != "v0.2.0" {
		t.Fatalf("expected v0.2.0, got %s", mod.Version)
	}

	// Shut down server — cached catalog should still work
	srv.Close()

	mod, err = cc.Resolve(context.Background(), Dependency{Name: "k6/x/ext", Constrains: "*"})
	if err != nil {
		t.Fatalf("unexpected error resolving from cache: %v", err)
	}
	if mod.Version != "v0.2.0" {
		t.Fatalf("expected v0.2.0, got %s", mod.Version)
	}
}

func TestCachedCatalog_RefreshesAfterTTL(t *testing.T) {
	t.Parallel()

	entries := baseCatalog()
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		data := testCatalogJSON(entries)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(data) //nolint:errcheck
	}))
	defer srv.Close()

	cc, err := NewCachedCatalog(context.Background(), CachedCatalogConfig{
		Location: srv.URL,
		TTL:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Initial resolve
	mod, err := cc.Resolve(context.Background(), Dependency{Name: "k6", Constrains: "*"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mod.Version != "v0.2.0" {
		t.Fatalf("expected v0.2.0, got %s", mod.Version)
	}

	// Update server catalog with a new version
	mu.Lock()
	entries["k6"] = entry{Module: "go.k6.io/k6", Versions: []string{"v0.1.0", "v0.2.0", "v0.3.0"}}
	mu.Unlock()

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Should pick up v0.3.0
	mod, err = cc.Resolve(context.Background(), Dependency{Name: "k6", Constrains: "*"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mod.Version != "v0.3.0" {
		t.Fatalf("expected v0.3.0, got %s", mod.Version)
	}
}

func TestCachedCatalog_RefreshOnMiss(t *testing.T) {
	t.Parallel()

	entries := baseCatalog()
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		data := testCatalogJSON(entries)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(data) //nolint:errcheck
	}))
	defer srv.Close()

	cc, err := NewCachedCatalog(context.Background(), CachedCatalogConfig{
		Location: srv.URL,
		TTL:      time.Hour, // long TTL — refresh should be triggered by miss
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Try to resolve unknown dependency — should fail
	_, err = cc.Resolve(context.Background(), Dependency{Name: "k6/x/newdep", Constrains: "*"})
	if !errors.Is(err, ErrUnknownDependency) {
		t.Fatalf("expected ErrUnknownDependency, got %v", err)
	}

	// Add the dependency to the server
	mu.Lock()
	entries["k6/x/newdep"] = entry{Module: "go.k6.io/newdep", Versions: []string{"v0.1.0"}}
	mu.Unlock()

	// Now resolve should succeed via refresh-on-miss
	mod, err := cc.Resolve(context.Background(), Dependency{Name: "k6/x/newdep", Constrains: "*"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mod.Version != "v0.1.0" {
		t.Fatalf("expected v0.1.0, got %s", mod.Version)
	}
}

func TestCachedCatalog_RefreshOnMiss_RemoteDown(t *testing.T) {
	t.Parallel()

	entries := baseCatalog()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(testCatalogJSON(entries)) //nolint:errcheck
	}))

	cc, err := NewCachedCatalog(context.Background(), CachedCatalogConfig{
		Location: srv.URL,
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Shut down server
	srv.Close()

	// Resolve unknown dependency — refresh will fail, should return original error
	_, err = cc.Resolve(context.Background(), Dependency{Name: "k6/x/unknown", Constrains: "*"})
	if !errors.Is(err, ErrUnknownDependency) {
		t.Fatalf("expected ErrUnknownDependency, got %v", err)
	}
}

func TestCachedCatalog_StaleCatalogOnRefreshFailure(t *testing.T) {
	t.Parallel()

	entries := baseCatalog()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(testCatalogJSON(entries)) //nolint:errcheck
	}))

	cc, err := NewCachedCatalog(context.Background(), CachedCatalogConfig{
		Location: srv.URL,
		TTL:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Shut down server and wait for TTL to expire
	srv.Close()
	time.Sleep(100 * time.Millisecond)

	// Known dependency should still resolve from stale cache
	mod, err := cc.Resolve(context.Background(), Dependency{Name: "k6", Constrains: "*"})
	if err != nil {
		t.Fatalf("expected stale cache to serve known dep, got error: %v", err)
	}
	if mod.Version != "v0.2.0" {
		t.Fatalf("expected v0.2.0, got %s", mod.Version)
	}
}

func TestCachedCatalog_ConcurrentRefreshOnMiss(t *testing.T) {
	t.Parallel()

	entries := baseCatalog()
	var mu sync.Mutex
	var fetchCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetchCount.Add(1)
		mu.Lock()
		data := testCatalogJSON(entries)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(data) //nolint:errcheck
	}))
	defer srv.Close()

	cc, err := NewCachedCatalog(context.Background(), CachedCatalogConfig{
		Location: srv.URL,
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reset counter after initial fetch
	fetchCount.Store(0)

	// Add the dependency to the server before concurrent access
	mu.Lock()
	entries["k6/x/newdep"] = entry{Module: "go.k6.io/newdep", Versions: []string{"v0.1.0"}}
	mu.Unlock()

	// 10 goroutines all try to resolve the unknown dependency
	const goroutines = 10
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = cc.Resolve(context.Background(), Dependency{Name: "k6/x/newdep", Constrains: "*"})
		}(i)
	}
	wg.Wait()

	// All should succeed
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d got error: %v", i, err)
		}
	}

	// Server should have been hit only once for the refresh (not 10 times)
	count := fetchCount.Load()
	if count > 2 {
		// Allow up to 2: one goroutine might have triggered refresh-on-miss
		// while another was doing TTL check. But definitely not 10.
		t.Errorf("expected at most 2 refresh fetches, got %d", count)
	}
}

func TestCachedCatalog_BootFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := NewCachedCatalog(context.Background(), CachedCatalogConfig{
		Location: srv.URL,
		TTL:      time.Hour,
	})
	if err == nil {
		t.Fatal("expected error on boot failure, got nil")
	}
}

func TestCachedCatalog_EmptyLocation(t *testing.T) {
	t.Parallel()

	_, err := NewCachedCatalog(context.Background(), CachedCatalogConfig{
		Location: "",
		TTL:      time.Hour,
	})
	if err == nil {
		t.Fatal("expected error for empty location")
	}
}
