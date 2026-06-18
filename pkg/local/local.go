// Package local implements a local build service
package local

import (
	"context"
	"fmt"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/builder"
	"github.com/grafana/k6build/pkg/catalog"
	"github.com/grafana/k6build/pkg/store/file"
)

// Opts local builder options
type Opts = builder.Opts

// GoOpts Go build options
type GoOpts = builder.GoOpts

// Config defines the configuration for a Local build service
type Config struct {
	Opts
	// path to catalog's json file. Can be a file path or a URL
	Catalog string
	// Catalogs maps a k6 module path to the catalog URL to use for that module path.
	// If a module path is not in this map, Catalog is used as the fallback.
	Catalogs map[string]string
	// path to object store dir
	StoreDir string
}

// NewBuildService creates a local build service using the given configuration
func NewBuildService(ctx context.Context, config Config) (k6build.BuildService, error) {
	store, err := file.NewFileStore(config.StoreDir)
	if err != nil {
		return nil, k6build.NewWrappedError(builder.ErrInitializingBuilder, err)
	}

	// A local build is a one-shot operation, so the catalogs are loaded directly
	// without caching.
	ctlg, err := catalog.NewCatalog(ctx, config.Catalog)
	if err != nil {
		return nil, k6build.NewWrappedError(builder.ErrInitializingBuilder, err)
	}

	catalogs := make(map[string]catalog.Catalog, len(config.Catalogs))
	for modPath, location := range config.Catalogs {
		c, err := catalog.NewCatalog(ctx, location)
		if err != nil {
			return nil, k6build.NewWrappedError(builder.ErrInitializingBuilder,
				fmt.Errorf("catalog for %s: %w", modPath, err))
		}
		catalogs[modPath] = c
	}

	return builder.New(ctx, builder.Config{
		Opts:     config.Opts,
		Catalog:  ctlg,
		Catalogs: catalogs,
		Store:    store,
	})
}
