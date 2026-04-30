// Package builder implements a build service
package builder

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/grafana/k6foundry"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/api"
	"github.com/grafana/k6build/pkg/catalog"
	"github.com/grafana/k6build/pkg/lock"
	"github.com/grafana/k6build/pkg/store"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	k6DependencyName = "k6"

	opRe    = `(?<operator>[=|~|>|<|\^|>=|<=|!=]){0,1}(?:\s*)`
	verRe   = `(?P<version>[v|V](?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*))`
	buildRe = `(?:[+|-|])(?P<build>(?:[0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))`

	defaultLockBackoff = time.Second
)

var (
	ErrBuildSemverNotAllowed = errors.New("semvers with build metadata not allowed") //nolint:revive
	ErrInitializingBuilder   = errors.New("initializing builder")

	constrainRe = regexp.MustCompile(opRe + verRe + buildRe)

	tracer = otel.Tracer("github.com/grafana/k6build/pkg/builder") //nolint:gochecknoglobals
)

// GoOpts defines the options for the go build environment
type GoOpts = k6foundry.GoOpts

// FoundryFactory is a function that creates a FoundryFactory
type FoundryFactory interface {
	NewFoundry(ctx context.Context, opts k6foundry.NativeFoundryOpts) (k6foundry.Foundry, error)
}

// FoundryFactoryFunction defines a function that implements the FoundryFactory interface
type FoundryFactoryFunction func(context.Context, k6foundry.NativeFoundryOpts) (k6foundry.Foundry, error)

// NewFoundry implements the Foundry interface
func (f FoundryFactoryFunction) NewFoundry(
	ctx context.Context,
	opts k6foundry.NativeFoundryOpts,
) (k6foundry.Foundry, error) {
	return f(ctx, opts)
}

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
	Catalog string
	// Catalogs maps a k6 module path (e.g. "go.k6.io/k6/v2") to the catalog URL
	// to use when a build request specifies that module path. If a module path is
	// not in this map, Catalog is used as the fallback.
	Catalogs    map[string]string
	Store       store.ObjectStore
	Foundry     FoundryFactory
	Registerer  prometheus.Registerer
	Lock        lock.Lock
	LockBackoff time.Duration
}

// ParseCatalogs parses a slice of "module=url" strings (as supplied by the
// --catalog-for CLI flag) into the map expected by Config.Catalogs.
func ParseCatalogs(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil //nolint:nilnil
	}

	catalogs := make(map[string]string, len(entries))

	for _, entry := range entries {
		mod, url, ok := strings.Cut(entry, "=")
		if !ok || mod == "" || url == "" {
			return nil, fmt.Errorf("invalid catalog entry %q: expected module=url", entry)
		}

		catalogs[mod] = url
	}

	return catalogs, nil
}

// Builder implements the BuildService interface
type Builder struct {
	opts        Opts
	catalog     string
	catalogs    map[string]string
	store       store.ObjectStore
	foundry     FoundryFactory
	metrics     *metrics
	lock        lock.Lock
	lockBackoff time.Duration
}

// New returns a new instance of Builder given a BuilderConfig
func New(_ context.Context, config Config) (*Builder, error) {
	if config.Catalog == "" {
		return nil, k6build.NewWrappedError(ErrInitializingBuilder, errors.New("catalog cannot be nil"))
	}

	if config.Store == nil {
		return nil, k6build.NewWrappedError(ErrInitializingBuilder, errors.New("store cannot be nil"))
	}

	foundry := config.Foundry
	if foundry == nil {
		foundry = FoundryFactoryFunction(k6foundry.NewNativeFoundry)
	}

	metrics := newMetrics()
	if config.Registerer != nil {
		err := metrics.register(config.Registerer)
		if err != nil {
			return nil, k6build.NewWrappedError(ErrInitializingBuilder, err)
		}
	}

	buildLock := config.Lock
	if buildLock == nil {
		buildLock = lock.NewMemoryLock()
	}

	lockBackoff := config.LockBackoff
	if lockBackoff == 0 {
		lockBackoff = defaultLockBackoff
	}
	return &Builder{
		catalog:     config.Catalog,
		catalogs:    config.Catalogs,
		opts:        config.Opts,
		store:       config.Store,
		foundry:     foundry,
		metrics:     metrics,
		lock:        buildLock,
		lockBackoff: lockBackoff,
	}, nil
}

// Build builds a custom k6 binary with dependencies
func (b *Builder) Build( //nolint:funlen
	ctx context.Context,
	platform string,
	k6ModPath string,
	k6Constrains string,
	deps []k6build.Dependency,
) (artifact k6build.Artifact, buildErr error) {
	ctx, span := tracer.Start(ctx, "Builder.Build", trace.WithAttributes(
		attribute.String("k6build.platform", platform),
		attribute.String("k6build.k6_constrains", k6Constrains),
		attribute.Int("k6build.deps_count", len(deps)),
	))
	defer func() {
		if buildErr != nil {
			span.RecordError(buildErr)
			span.SetStatus(codes.Error, buildErr.Error())
		}
		span.End()
	}()

	b.metrics.requestCounter.Inc()

	requestTimer := prometheus.NewTimer(b.metrics.requestTimeHistogram)
	defer func() {
		// record time only if request was successful
		if buildErr == nil {
			requestTimer.ObserveDuration()
		}
	}()

	// check if the platform is valid early to avoid unnecessary work
	_, err := k6foundry.ParsePlatform(platform)
	if err != nil {
		b.metrics.buildsInvalidCounter.Inc()
		return k6build.Artifact{}, k6build.NewWrappedError(k6build.ErrInvalidParameters, err)
	}

	resolved, err := b.resolveDependencies(ctx, k6ModPath, k6Constrains, deps)
	if err != nil {
		b.metrics.buildsInvalidCounter.Inc()
		return k6build.Artifact{}, err
	}

	id := generateArtifactID(platform, resolved)
	span.SetAttributes(attribute.String("k6build.artifact_id", id))

	// Try to get the object, if not found, try to acquire a build lock.
	// If the build lock is not acquired, assume some other builder is building the binary.
	// Sleep and retry.
	// If nocache is set, skip the cache lookup and go straight to building.
	noCache := api.NoCache(ctx)
	var artifactObject store.Object
	for {
		if !noCache {
			artifactObject, err = b.store.Get(ctx, id)
			if err == nil {
				b.metrics.storeHitsCounter.Inc()
				span.SetAttributes(attribute.Bool("k6build.cache_hit", true))

				return k6build.Artifact{
					ID:           id,
					Checksum:     artifactObject.Checksum,
					URL:          artifactObject.URL,
					Dependencies: resolvedVersions(resolved),
					Platform:     platform,
				}, nil
			}

			if !errors.Is(err, store.ErrObjectNotFound) {
				return k6build.Artifact{}, k6build.NewWrappedError(k6build.ErrAccessingArtifact, err)
			}
		}

		acquired, unlock, err := b.lock.Try(ctx, id)
		if err != nil {
			return k6build.Artifact{}, k6build.NewWrappedError(k6build.ErrAccessingArtifact, err)
		}

		if acquired {
			defer unlock(ctx) //nolint:errcheck
			break
		}

		time.Sleep(b.lockBackoff)
	}

	artifactBuffer := &bytes.Buffer{}

	buildInfo, err := b.buildArtifact(ctx, platform, resolved, artifactBuffer)
	if err != nil {
		return k6build.Artifact{}, err
	}

	artifactObject, err = b.store.Put(ctx, id, artifactBuffer)

	// if there was a conflict creating the object, get returns the object
	if errors.Is(err, store.ErrDuplicateObject) || (err != nil && strings.Contains(err.Error(), "duplicate object")) {
		artifactObject, err = b.store.Get(ctx, id)
	}

	if err != nil {
		return k6build.Artifact{}, k6build.NewWrappedError(k6build.ErrAccessingArtifact, err)
	}

	warnings := make([]string, 0, len(buildInfo.Warnings))
	for _, w := range buildInfo.Warnings {
		warnings = append(warnings, fmt.Sprintf("[%s] %s", w.Code, w.Message))
	}

	return k6build.Artifact{
		ID:           id,
		Checksum:     artifactObject.Checksum,
		URL:          artifactObject.URL,
		Dependencies: resolvedVersions(resolved),
		Platform:     platform,
		K6ModPath:    buildInfo.K6ModPath,
		Warnings:     warnings,
	}, nil
}

// Resolve returns the version that resolve the given dependencies
func (b *Builder) Resolve(
	ctx context.Context,
	k6ModPath string,
	k6Constrains string,
	deps []k6build.Dependency,
) (map[string]string, error) {
	resolved, err := b.resolveDependencies(ctx, k6ModPath, k6Constrains, deps)
	if err != nil {
		return nil, err
	}

	return resolvedVersions(resolved), nil
}

func (b *Builder) resolveDependencies(
	ctx context.Context,
	k6ModPath string,
	k6Constrains string,
	deps []k6build.Dependency,
) (map[string]catalog.Module, error) {
	ctx, span := tracer.Start(ctx, "Builder.resolveDependencies")
	defer span.End()

	// Default to v1 module path if not specified.
	if k6ModPath == "" {
		k6ModPath = k6build.K6ModPath
	}

	catalogURL := b.catalog
	if url, ok := b.catalogs[k6ModPath]; ok && url != "" {
		catalogURL = url
	}

	ctlg, err := catalog.NewCatalog(ctx, catalogURL)
	if err != nil {
		return nil, k6build.NewWrappedError(k6build.ErrCatalog, err)
	}

	resolved := map[string]catalog.Module{}

	// check if it is a semver of the form v0.0.0+<build>
	// if it is, we don't check with the catalog, but instead we use
	// the build metadata as version when building this module
	var k6Mod catalog.Module
	buildMetadata, err := hasBuildMetadata(k6Constrains)
	if err != nil {
		return nil, k6build.NewWrappedError(k6build.ErrInvalidParameters, err)
	}
	if buildMetadata != "" {
		if !b.opts.AllowBuildSemvers {
			return nil, k6build.NewWrappedError(k6build.ErrInvalidParameters, ErrBuildSemverNotAllowed)
		}
		// use a semantic version for the build metadata
		k6Mod = catalog.Module{Path: k6ModPath, Version: "v0.0.0+" + buildMetadata}
	} else {
		k6Mod, err = ctlg.Resolve(ctx, catalog.Dependency{Name: k6DependencyName, Constrains: k6Constrains})
		if err != nil {
			return nil, k6build.NewWrappedError(k6build.ErrInvalidParameters, err)
		}
	}
	resolved[k6DependencyName] = k6Mod

	for _, d := range deps {
		m, err := ctlg.Resolve(ctx, catalog.Dependency{Name: d.Name, Constrains: d.Constraints})
		if err != nil {
			return nil, k6build.NewWrappedError(k6build.ErrInvalidParameters, err)
		}
		resolved[d.Name] = m
	}

	return resolved, nil
}

// hasBuildMetadata checks if the constrain references a version with a build metadata.
// and if so, checks if the version is valid. Only v0.0.0 is allowed.
// E.g.  v0.0.0+effa45f
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
		return "", fmt.Errorf("only exact match is allowed for versions with build metadata")
	}

	if ver != "v0.0.0" {
		return "", fmt.Errorf("version with build metadata must start with v0.0.0")
	}
	return build, nil
}

// generateArtifactID generates a unique identifier for a build
func generateArtifactID(platform string, deps map[string]catalog.Module) string {
	hashData := bytes.Buffer{}
	hashData.WriteString(platform)

	// add k6 as the first dependency
	fmt.Fprintf(&hashData, ":%s%s", k6DependencyName, deps[k6DependencyName].Version)

	// add the other dependencies
	for _, d := range slices.Sorted(maps.Keys(deps)) {
		if d == k6DependencyName {
			continue
		}
		fmt.Fprintf(&hashData, ":%s%s", d, deps[d].Version)
	}

	return fmt.Sprintf("%x", sha1.Sum(hashData.Bytes())) //nolint:gosec
}

func resolvedVersions(deps map[string]catalog.Module) map[string]string {
	versions := map[string]string{}

	for d, m := range deps {
		versions[d] = m.Version
	}

	return versions
}

func (b *Builder) buildArtifact(
	ctx context.Context,
	platform string,
	deps map[string]catalog.Module,
	artifactBuffer io.Writer,
) (*k6foundry.BuildInfo, error) {
	ctx, span := tracer.Start(ctx, "Builder.buildArtifact", trace.WithAttributes(
		attribute.String("k6build.platform", platform),
	))
	defer span.End()

	// already checked the platform is valid, should be safe to ignore the error
	buildPlatform, _ := k6foundry.ParsePlatform(platform)

	k6Mod := deps[k6DependencyName]
	k6Version := k6Mod.Version

	mods := []k6foundry.Module{}
	cgoEnabled := false
	for k, m := range deps {
		if k == k6DependencyName {
			continue
		}
		// Strip v0.0.0+ build metadata prefix so go get receives a bare SHA.
		version := m.Version
		if _, sha, found := strings.Cut(version, "+"); found {
			version = sha
		}
		mods = append(mods, k6foundry.Module{Path: m.Path, Version: version})
		cgoEnabled = cgoEnabled || m.Cgo
	}

	// set CGO_ENABLED if any of the dependencies require it
	env := b.opts.Env
	if cgoEnabled {
		if env == nil {
			env = map[string]string{}
		}
		env["CGO_ENABLED"] = "1"
	}

	builderOpts := k6foundry.NativeFoundryOpts{
		GoOpts: k6foundry.GoOpts{
			Env:       env,
			CopyGoEnv: b.opts.CopyGoEnv,
		},
	}
	// Extract the major version suffix from the k6 module path (e.g. "v2" from "go.k6.io/k6/v2")
	// so k6foundry uses the correct import path when the k6 version is a git SHA.
	if idx := strings.LastIndex(k6Mod.Path, "/"); idx >= 0 {
		if suffix := k6Mod.Path[idx+1:]; strings.HasPrefix(suffix, "v") && len(suffix) > 1 {
			builderOpts.K6MajorVersion = suffix
		}
	}
	if b.opts.Verbose {
		builderOpts.Stdout = os.Stdout //nolint:forbidigo
		builderOpts.Stderr = os.Stderr //nolint:forbidigo
	}

	builder, err := b.foundry.NewFoundry(ctx, builderOpts)
	if err != nil {
		return nil, k6build.NewWrappedError(ErrInitializingBuilder, err)
	}

	// if the version is a build version, we need the build metadata and ignore the version
	// as go does not accept semvers with build metadata
	_, build, found := strings.Cut(k6Version, "+")
	if found {
		k6Version = build
	}

	b.metrics.buildCounter.Inc()
	buildTimer := prometheus.NewTimer(b.metrics.buildTimeHistogram)

	buildInfo, err := builder.Build(ctx, buildPlatform, k6Version, mods, nil, []string{}, artifactBuffer)
	if err != nil {
		b.metrics.buildsFailedCounter.Inc()
		return nil, k6build.NewWrappedError(k6build.ErrBuildFailed, err)
	}

	buildTimer.ObserveDuration()

	// Cross-check: warn if the module path k6foundry actually used differs from what
	// the catalog resolved. This should not happen in normal operation but is a useful
	// signal if catalog entries are misconfigured.
	if buildInfo.K6ModPath != "" && buildInfo.K6ModPath != k6Mod.Path {
		span.SetAttributes(attribute.String("k6build.k6_mod_path_mismatch",
			fmt.Sprintf("catalog=%s foundry=%s", k6Mod.Path, buildInfo.K6ModPath)))
	}

	return buildInfo, nil
}
