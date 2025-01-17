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
)

const (
	k6Dep  = "k6"
	k6Path = "go.k6.io/k6"

	opRe    = `(?<operator>[=|~|>|<|\^|>=|<=|!=]){0,1}(?:\s*)`
	verRe   = `(?P<version>[v|V](?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*))`
	buildRe = `(?:[+|-|])(?P<build>(?:[0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))`
)

var (
	ErrAccessingArtifact     = errors.New("accessing artifact")                      //nolint:revive
	ErrBuildingArtifact      = errors.New("building artifact")                       //nolint:revive
	ErrInitializingBuilder   = errors.New("initializing builder")                    //nolint:revive
	ErrInvalidParameters     = errors.New("invalid build parameters")                //nolint:revive
	ErrBuildSemverNotAllowed = errors.New("semvers with build metadata not allowed") //nolint:revive

	constrainRe = regexp.MustCompile(opRe + verRe + buildRe)
)

// GoOpts defines the options for the go build environment
type GoOpts = k6foundry.GoOpts

// Foundry is a function that creates a Foundry Builder
type Foundry interface {
	NewBuilder(ctx context.Context, opts k6foundry.NativeBuilderOpts) (k6foundry.Builder, error)
}

// FoundryFunction defines a function that implements the Foundry interface
type FoundryFunction func(context.Context, k6foundry.NativeBuilderOpts) (k6foundry.Builder, error)

// NewBuilder implements the Foundry interface
func (f FoundryFunction) NewBuilder(ctx context.Context, opts k6foundry.NativeBuilderOpts) (k6foundry.Builder, error) {
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
	Catalog catalog.Catalog
	Store   store.ObjectStore
	Foundry Foundry
}

// Builder implements the BuildService interface
type Builder struct {
	opts    Opts
	catalog catalog.Catalog
	store   store.ObjectStore
	mutexes sync.Map
	foundry Foundry
}

// New returns a new instance of Builder given a BuilderConfig
func New(_ context.Context, config Config) (*Builder, error) {
	if config.Catalog == nil {
		return nil, k6build.NewWrappedError(ErrInitializingBuilder, errors.New("catalog cannot be nil"))
	}

	if config.Store == nil {
		return nil, k6build.NewWrappedError(ErrInitializingBuilder, errors.New("store cannot be nil"))
	}

	foundry := config.Foundry
	if foundry == nil {
		foundry = FoundryFunction(k6foundry.NewNativeBuilder)
	}
	return &Builder{
		catalog: config.Catalog,
		opts:    config.Opts,
		store:   config.Store,
		foundry: foundry,
	}, nil
}

// Build builds a custom k6 binary with dependencies
func (b *Builder) Build( //nolint:funlen
	ctx context.Context,
	platform string,
	k6Constrains string,
	deps []k6build.Dependency,
) (k6build.Artifact, error) {
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

	builder, err := b.foundry.NewBuilder(ctx, builderOpts)
	if err != nil {
		return k6build.Artifact{}, k6build.NewWrappedError(ErrInitializingBuilder, err)
	}

	artifactBuffer := &bytes.Buffer{}
	buildInfo, err := builder.Build(ctx, buildPlatform, k6Mod.Version, mods, []string{}, artifactBuffer)
	if err != nil {
		return k6build.Artifact{}, k6build.NewWrappedError(ErrAccessingArtifact, err)
	}

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
