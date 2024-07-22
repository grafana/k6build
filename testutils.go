// Package k6build offers utility functions for testing
package k6build

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"

	"github.com/grafana/k6catalog"
	"github.com/grafana/k6foundry"
	"github.com/grafana/k6foundry/pkg/testutils/goproxy"
)

// SetupTestLocalBuildService setups a local build service for testing
func SetupTestLocalBuildService(config LocalBuildServiceConfig) (BuildService, error) {
	modules := []struct {
		path    string
		version string
		source  string
	}{
		{
			path:    "go.k6.io/k6",
			version: "v0.1.0",
			source:  filepath.Join("testdata", "deps", "k6"),
		},
		{
			path:    "go.k6.io/k6",
			version: "v0.2.0",
			source:  filepath.Join("testdata", "deps", "k6"),
		},
		{
			path:    "go.k6.io/k6ext",
			version: "v0.1.0",
			source:  filepath.Join("testdata", "deps", "k6ext"),
		},
		{
			path:    "go.k6.io/k6ext",
			version: "v0.2.0",
			source:  filepath.Join("testdata", "deps" , "k6ext"),
		},
		{
			path:    "go.k6.io/k6ext2",
			version: "v0.1.0",
			source:  filepath.Join("testdata", "deps", "k6ext2"),
		},
	}

	// creates a goproxy that serves the given modules
	proxy := goproxy.NewGoProxy()
	for _, m := range modules {
		err := proxy.AddModVersion(m.path, m.version, m.source)
		if err != nil {
			return nil, fmt.Errorf("setup %w", err)
		}
	}

	goproxySrv := httptest.NewServer(proxy)

	opts := k6foundry.NativeBuilderOpts{
		GoOpts: k6foundry.GoOpts{
			CopyGoEnv: true,
			Env: map[string]string{
				"GOPROXY":   goproxySrv.URL,
				"GONOPROXY": "none",
				"GOPRIVATE": "go.k6.io",
				"GONOSUMDB": "go.k6.io",
			},
			TmpCache: true,
		},
	}

	builder, err := k6foundry.NewNativeBuilder(context.Background(), opts)
	if err != nil {
		return nil, fmt.Errorf("setting up test builder %w", err)
	}

	catalog, err := k6catalog.NewCatalogFromJSON(config.Catalog)
	if err != nil {
		return nil, fmt.Errorf("setting up test builder %w", err)
	}

	cache, err := NewFileCache(config.CacheDir)
	if err != nil {
		return nil, fmt.Errorf("creating temp cache %w", err)
	}

	buildsrv := NewBuildService(catalog, builder, cache)

	return buildsrv, nil
}
