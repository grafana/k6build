// Package builder implements a build service
package builder

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"sync"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/catalog"
	"github.com/grafana/k6build/pkg/store"
	"github.com/grafana/k6foundry"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	k6Dep  = "k6"
	k6Path = "go.k6.io/k6"

	opRe    = `(?<operator>[=|~|>|<|\^|>=|<=|!=]){0,1}(?:\s*)`
	verRe   = `(?P<version>[v|V](?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*))`
	buildRe = `(?:[+|-|])(?P<build>(?:[0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))`

	metricsNamespace = "k6build"
)

// FIXME: this is a temporary solution to avoid a problem re-registering the metrics
// when running tests
func init() {
	var err error
	buildMetrics, err = newMetrics()
	if err != nil {
		panic(fmt.Sprintf("error creating metrics: %v", err))
	}
}

var (
	ErrAccessingArtifact     = errors.New("accessing artifact")                      //nolint:revive
	ErrBuildingArtifact      = errors.New("building artifact")                       //nolint:revive
	ErrInitializingBuilder   = errors.New("initializing builder")                    //nolint:revive
	ErrInvalidParameters     = errors.New("invalid build parameters")                //nolint:revive
	ErrBuildSemverNotAllowed = errors.New("semvers with build metadata not allowed") //nolint:revive

	constrainRe = regexp.MustCompile(opRe + verRe + buildRe)

	buildMetrics *metrics //nolint:gochecknoglobals
)

// GoOpts defines the options for the go build environment
type GoOpts = k6foundry.GoOpts

// Opts defines the options for configuring the builder
type Opts struct {
	// Allow semvers with build metadata
	AllowBuildSemvers bool
	// Generate build output
	Verbose bool
	// Build environment options
	GoOpts
}

// Config defines the configuration for a Builder
type Config struct {
	Opts    Opts
	Catalog catalog.Catalog
	Store   store.ObjectStore
}

type metrics struct {
	requestCounter       prometheus.Counter
	buildCounter         prometheus.Counter
	storeHitsCounter     prometheus.Counter
	buildsFailedCounter  prometheus.Counter
	buildsInvalidCounter prometheus.Counter
	buildTimeHistogram   prometheus.Histogram
}

func newMetrics() (*metrics, error) {
	requestCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "request_total",
		Help:      "The total number of builds requests",
	})

	if err := prometheus.Register(requestCounter); err != nil {
		return nil, err
	}

	buildCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "builds_total",
		Help:      "The total number of builds",
	})
	if err := prometheus.Register(buildCounter); err != nil {
		return nil, err
	}

	buildsFailedCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "builds_failed_total",
		Help:      "The total number of failed builds",
	})

	if err := prometheus.Register(buildsFailedCounter); err != nil {
		return nil, err
	}

	buildsInvalidCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "builds_invalid_total",
		Help:      "The total number of builds with invalid parameters",
	})

	if err := prometheus.Register(buildsInvalidCounter); err != nil {
		return nil, err
	}

	storeHitsCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "object_store_hits_total",
		Help:      "The total number of object store hits",
	})
	if err := prometheus.Register(storeHitsCounter); err != nil {
		return nil, err
	}

	buildTimeHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      "build_duration_seconds",
		Help:      "The duration of the build in seconds",
		Buckets:   []float64{1, 2.5, 5, 10, 20, 30, 60, 120, 300},
	})
	if err := prometheus.Register(buildTimeHistogram); err != nil {
		return nil, err
	}

	return &metrics{
		requestCounter:       requestCounter,
		buildCounter:         buildCounter,
		buildsFailedCounter:  buildsFailedCounter,
		buildsInvalidCounter: buildsInvalidCounter,
		storeHitsCounter:     storeHitsCounter,
		buildTimeHistogram:   buildTimeHistogram,
	}, nil
}

// Builder implements the BuildService interface
type Builder struct {
	opts    Opts
	catalog catalog.Catalog
	store   store.ObjectStore
	mutexes sync.Map
	metrics *metrics
}

// New returns a new instance of Builder given a BuilderConfig
func New(_ context.Context, config Config) (*Builder, error) {
	if config.Catalog == nil {
		return nil, k6build.NewWrappedError(ErrInitializingBuilder, errors.New("catalog cannot be nil"))
	}

	if config.Store == nil {
		return nil, k6build.NewWrappedError(ErrInitializingBuilder, errors.New("store cannot be nil"))
	}

	return &Builder{
		catalog: config.Catalog,
		opts:    config.Opts,
		store:   config.Store,
		metrics: buildMetrics,
	}, nil
}

// Build builds a custom k6 binary with dependencies
func (b *Builder) Build( //nolint:funlen
	ctx context.Context,
	platform string,
	k6Constrains string,
	deps []k6build.Dependency,
) (artifact k6build.Artifact, buildErr error) {
	// FIXME: this is a temporary solution because the logic has many paths that return
	// an invalid parameters error and we need to increment the metrics in all of them
	defer func() {
		if errors.Is(buildErr, ErrInvalidParameters) {
			b.metrics.buildsInvalidCounter.Inc()
		}
	}()

	b.metrics.requestCounter.Inc()

	buildPlatform, err := k6foundry.ParsePlatform(platform)
	if err != nil {
		return k6build.Artifact{}, k6build.NewWrappedError(ErrInvalidParameters, err)
	}

	// sort dependencies to ensure idempotence of build
	sort.Slice(deps, func(i, j int) bool { return deps[i].Name < deps[j].Name })
	resolved := map[string]string{}

	// check if it is a semver of the form v0.0.0+<build>
	// if it is, we don't check with the catalog, but instead we use
	// the build metadata as version when building this module
	// the build process will return the actual version built in the build info
	// and we can check that version with the catalog
	var k6Mod catalog.Module
	buildMetadata, err := hasBuildMetadata(k6Constrains)
	if err != nil {
		return k6build.Artifact{}, err
	}
	if buildMetadata != "" {
		if !b.opts.AllowBuildSemvers {
			return k6build.Artifact{}, k6build.NewWrappedError(ErrInvalidParameters, ErrBuildSemverNotAllowed)
		}
		k6Mod = catalog.Module{Path: k6Path, Version: buildMetadata}
	} else {
		k6Mod, err = b.catalog.Resolve(ctx, catalog.Dependency{Name: k6Dep, Constrains: k6Constrains})
		if err != nil {
			return k6build.Artifact{}, k6build.NewWrappedError(ErrInvalidParameters, err)
		}
	}
	resolved[k6Dep] = k6Mod.Version

	mods := []k6foundry.Module{}
	cgoEnabled := false
	for _, d := range deps {
		m, modErr := b.catalog.Resolve(ctx, catalog.Dependency{Name: d.Name, Constrains: d.Constraints})
		if modErr != nil {
			return k6build.Artifact{}, k6build.NewWrappedError(ErrInvalidParameters, modErr)
		}
		mods = append(mods, k6foundry.Module{Path: m.Path, Version: m.Version})
		resolved[d.Name] = m.Version
		cgoEnabled = cgoEnabled || m.Cgo
	}

	// generate id form sorted list of dependencies
	hashData := bytes.Buffer{}
	hashData.WriteString(platform)
	hashData.WriteString(fmt.Sprintf(":k6%s", k6Mod.Version))
	for _, d := range deps {
		hashData.WriteString(fmt.Sprintf(":%s%s", d, resolved[d.Name]))
	}
	id := fmt.Sprintf("%x", sha1.Sum(hashData.Bytes())) //nolint:gosec

	unlock := b.lockArtifact(id)
	defer unlock()

	artifactObject, err := b.store.Get(ctx, id)
	if err == nil {
		b.metrics.storeHitsCounter.Inc()

		return k6build.Artifact{
			ID:           id,
			Checksum:     artifactObject.Checksum,
			URL:          artifactObject.URL,
			Dependencies: resolved,
			Platform:     platform,
		}, nil
	}

	if !errors.Is(err, store.ErrObjectNotFound) {
		return k6build.Artifact{}, k6build.NewWrappedError(ErrAccessingArtifact, err)
	}

	// set CGO_ENABLED if any of the dependencies require it
	env := b.opts.Env
	if cgoEnabled {
		if env == nil {
			env = map[string]string{}
		}
		env["CGO_ENABLED"] = "1"
	}

	builderOpts := k6foundry.NativeBuilderOpts{
		GoOpts: k6foundry.GoOpts{
			Env:       env,
			CopyGoEnv: b.opts.CopyGoEnv,
		},
	}
	if b.opts.Verbose {
		builderOpts.Stdout = os.Stdout
		builderOpts.Stderr = os.Stderr
	}

	builder, err := k6foundry.NewNativeBuilder(ctx, builderOpts)
	if err != nil {
		return k6build.Artifact{}, k6build.NewWrappedError(ErrInitializingBuilder, err)
	}
	b.metrics.buildCounter.Inc()
	buildTimer := prometheus.NewTimer(b.metrics.buildTimeHistogram)

	artifactBuffer := &bytes.Buffer{}
	buildInfo, err := builder.Build(ctx, buildPlatform, k6Mod.Version, mods, []string{}, artifactBuffer)
	if err != nil {
		b.metrics.buildsFailedCounter.Inc()
		return k6build.Artifact{}, k6build.NewWrappedError(ErrAccessingArtifact, err)
	}

	buildTimer.ObserveDuration()

	// if the version has a build metadata, we must use the actual version built
	// TODO: check this version is supported
	if buildMetadata != "" {
		resolved[k6Dep] = buildInfo.ModVersions[k6Mod.Path]
	}

	artifactObject, err = b.store.Put(ctx, id, artifactBuffer)
	if err != nil {
		return k6build.Artifact{}, k6build.NewWrappedError(ErrAccessingArtifact, err)
	}

	return k6build.Artifact{
		ID:           id,
		Checksum:     artifactObject.Checksum,
		URL:          artifactObject.URL,
		Dependencies: resolved,
		Platform:     platform,
	}, nil
}

// lockArtifact obtains a mutex used to prevent concurrent builds of the same artifact and
// returns a function that will unlock the mutex associated to the given id in the object store.
// The lock is also removed from the map. Subsequent calls will get another lock on the same
// id but this is safe as the object should already be in the object store and no further
// builds are needed.
func (b *Builder) lockArtifact(id string) func() {
	value, _ := b.mutexes.LoadOrStore(id, &sync.Mutex{})
	mtx, _ := value.(*sync.Mutex)
	mtx.Lock()

	return func() {
		b.mutexes.Delete(id)
		mtx.Unlock()
	}
}

// hasBuildMetadata checks if the constrain references a version with a build metadata.
// E.g.  v0.1.0+build-effa45f
func hasBuildMetadata(constrain string) (string, error) {
	opInx := constrainRe.SubexpIndex("operator")
	verIdx := constrainRe.SubexpIndex("version")
	preIdx := constrainRe.SubexpIndex("build")
	matches := constrainRe.FindStringSubmatch(constrain)

	if matches == nil {
		return "", nil
	}

	op := matches[opInx]
	ver := matches[verIdx]
	build := matches[preIdx]

	if op != "" && op != "=" {
		return "", k6build.NewWrappedError(
			ErrInvalidParameters,
			fmt.Errorf("only exact match is allowed for versions with build metadata"),
		)
	}

	if ver != "v0.0.0" {
		return "", k6build.NewWrappedError(
			ErrInvalidParameters,
			fmt.Errorf("version with build metadata must start with v0.0.0"),
		)
	}
	return build, nil
}
