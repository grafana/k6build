package builder

import (
	"context"
	"errors"
	"io"
	"maps"
	"path/filepath"
	"testing"

	"github.com/grafana/k6foundry"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/store/file"
)

func TestPseudoVersionCommit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title     string
		constrain string
		expect    string
		expectErr bool
	}{
		{
			title:     "pseudo-version based on a tagged release",
			constrain: "v1.7.2-0.20260603164357-0c72fa6d4511",
			expect:    "0c72fa6d4511",
		},
		{
			title:     "pseudo-version with no base version",
			constrain: "v0.0.0-20260603164357-0c72fa6d4511",
			expect:    "0c72fa6d4511",
		},
		{
			title:     "pseudo-version based on a pre-release",
			constrain: "v1.7.2-rc.0.20260603164357-0c72fa6d4511",
			expect:    "0c72fa6d4511",
		},
		{
			title:     "pseudo-version with a leading = operator",
			constrain: "=v1.7.2-0.20260603164357-0c72fa6d4511",
			expect:    "0c72fa6d4511",
		},
		{
			title:     "pseudo-version with a non-exact operator",
			constrain: ">v1.7.2-0.20260603164357-0c72fa6d4511",
			expectErr: true,
		},
		{
			title:     "exact semver",
			constrain: "v1.7.2",
			expect:    "",
		},
		{
			title:     "semver range",
			constrain: ">v1.7.2",
			expect:    "",
		},
		{
			title:     "wildcard",
			constrain: "*",
			expect:    "",
		},
		{
			title:     "build metadata semver",
			constrain: "v0.0.0+0c72fa6d4511",
			expect:    "",
		},
		{
			title:     "plain pre-release (not a pseudo-version)",
			constrain: "v1.0.0-rc1",
			expect:    "",
		},
		{
			title:     "empty",
			constrain: "",
			expect:    "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			commit, err := pseudoVersionCommit(tc.constrain)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("pseudoVersionCommit(%q) expected an error, got nil", tc.constrain)
				}
				return
			}
			if err != nil {
				t.Fatalf("pseudoVersionCommit(%q) unexpected error: %v", tc.constrain, err)
			}
			if commit != tc.expect {
				t.Fatalf("pseudoVersionCommit(%q) = %q, want %q", tc.constrain, commit, tc.expect)
			}
		})
	}
}

// recordingFoundry captures the arguments passed to Build so tests can assert
// what the builder forwarded to the foundry.
type recordingFoundry struct {
	opts      k6foundry.NativeFoundryOpts
	k6Version string
	mods      []k6foundry.Module
	// modVersions, if set, is merged into the returned BuildInfo.ModVersions
	// (keyed by module path) to simulate the canonical versions go list -m reports.
	modVersions map[string]string
}

func (m *recordingFoundry) Build(
	_ context.Context,
	platform k6foundry.Platform,
	k6Version string,
	mods []k6foundry.Module,
	_ []k6foundry.Module,
	_ []string,
	_ io.Writer,
) (*k6foundry.BuildInfo, error) {
	m.k6Version = k6Version
	m.mods = mods

	modVersions := make(map[string]string, len(mods))
	for _, mod := range mods {
		modVersions[mod.Path] = mod.Version
	}
	maps.Copy(modVersions, m.modVersions)
	return &k6foundry.BuildInfo{
		Platform:    platform.String(),
		ModVersions: modVersions,
	}, nil
}

// newRecordingBuilder creates a builder whose foundry records the build arguments.
// AllowBuildSemvers is configurable since pseudo-versions, like v0.0.0+<commit>
// build-metadata versions, build an arbitrary commit and are gated by it.
func newRecordingBuilder(t *testing.T, k6ModPath string, allowBuildSemvers bool) (*Builder, *recordingFoundry) {
	t.Helper()

	store, err := file.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("creating temporary object store %v", err)
	}

	rec := &recordingFoundry{}
	factory := FoundryFactoryFunction(func(_ context.Context, opts k6foundry.NativeFoundryOpts) (k6foundry.Foundry, error) {
		rec.opts = opts
		return rec, nil
	})

	catalog := "catalog.json"
	if k6ModPath == "go.k6.io/k6/v2" {
		catalog = "catalog-v2.json"
	}

	buildsrv, err := New(t.Context(), Config{
		Opts:    Opts{AllowBuildSemvers: allowBuildSemvers},
		Catalog: filepath.Join("testdata", catalog),
		Store:   store,
		Foundry: factory,
	})
	if err != nil {
		t.Fatalf("creating builder %v", err)
	}

	return buildsrv, rec
}

func TestResolvePseudoVersion(t *testing.T) {
	t.Parallel()

	// Resolve does not build, so the commit cannot be resolved to its canonical
	// pseudo-version; it is normalized to v0.0.0+<commit>.
	const (
		pseudo  = "v1.7.2-0.20260603164357-0c72fa6d4511"
		resolve = "v0.0.0+0c72fa6d4511"
	)

	buildsrv, _ := newRecordingBuilder(t, defaultK6ModPath, true)

	resolved, err := buildsrv.Resolve(t.Context(), defaultK6ModPath, pseudo, []k6build.Dependency{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := resolved["k6"]; got != resolve {
		t.Fatalf("expected k6 version %q, got %q", resolve, got)
	}
}

func TestResolvePseudoVersionWithOperator(t *testing.T) {
	t.Parallel()

	const (
		pseudo  = "=v1.7.2-0.20260603164357-0c72fa6d4511"
		resolve = "v0.0.0+0c72fa6d4511"
	)

	buildsrv, _ := newRecordingBuilder(t, defaultK6ModPath, true)

	resolved, err := buildsrv.Resolve(t.Context(), defaultK6ModPath, pseudo, []k6build.Dependency{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := resolved["k6"]; got != resolve {
		t.Fatalf("expected k6 version %q, got %q", resolve, got)
	}
}

func TestBuildPseudoVersion(t *testing.T) {
	t.Parallel()

	const (
		pseudo = "v1.7.2-0.20260603164357-0c72fa6d4511"
		commit = "0c72fa6d4511"
	)

	buildsrv, rec := newRecordingBuilder(t, defaultK6ModPath, true)
	// the build resolves the commit to its canonical pseudo-version
	rec.modVersions = map[string]string{defaultK6ModPath: pseudo}

	artifact, err := buildsrv.Build(t.Context(), platform(), defaultK6ModPath, pseudo, []k6build.Dependency{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// the artifact reports the version the build actually used
	if got := artifact.Dependencies["k6"]; got != pseudo {
		t.Fatalf("expected k6 version %q in artifact, got %q", pseudo, got)
	}

	// the foundry receives the bare commit as the build version
	if rec.k6Version != commit {
		t.Fatalf("expected foundry to receive k6 version %q, got %q", commit, rec.k6Version)
	}
}

func TestBuildPseudoVersionV2(t *testing.T) {
	t.Parallel()

	const (
		pseudo    = "v2.0.0-0.20260603164357-0c72fa6d4511"
		commit    = "0c72fa6d4511"
		k6ModPath = "go.k6.io/k6/v2"
	)

	buildsrv, rec := newRecordingBuilder(t, k6ModPath, true)
	rec.modVersions = map[string]string{k6ModPath: pseudo}

	artifact, err := buildsrv.Build(t.Context(), platform(), k6ModPath, pseudo, []k6build.Dependency{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := artifact.Dependencies["k6"]; got != pseudo {
		t.Fatalf("expected k6 version %q in artifact, got %q", pseudo, got)
	}

	if rec.k6Version != commit {
		t.Fatalf("expected foundry to receive k6 version %q, got %q", commit, rec.k6Version)
	}

	// the major version suffix must be derived from the v2 module path so the
	// foundry uses the correct import path for the git SHA.
	if rec.opts.K6MajorVersion != "v2" {
		t.Fatalf("expected K6MajorVersion %q, got %q", "v2", rec.opts.K6MajorVersion)
	}
}

// TestBuildBuildMetadata ensures that when the build reports no version for the
// module, the v0.0.0+<commit> build-metadata semver is reported as-is while the
// foundry receives the bare commit.
func TestBuildBuildMetadata(t *testing.T) {
	t.Parallel()

	const (
		buildMeta = "v0.0.0+0c72fa6d4511"
		commit    = "0c72fa6d4511"
	)

	buildsrv, rec := newRecordingBuilder(t, defaultK6ModPath, true)

	artifact, err := buildsrv.Build(t.Context(), platform(), defaultK6ModPath, buildMeta, []k6build.Dependency{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := artifact.Dependencies["k6"]; got != buildMeta {
		t.Fatalf("expected k6 version %q in artifact, got %q", buildMeta, got)
	}

	if rec.k6Version != commit {
		t.Fatalf("expected foundry to receive k6 version %q, got %q", commit, rec.k6Version)
	}
}

// TestBuildBuildMetadataCanonical ensures a v0.0.0+<commit> build-metadata semver is
// upgraded to the canonical Go pseudo-version reported by the build (go list -m).
func TestBuildBuildMetadataCanonical(t *testing.T) {
	t.Parallel()

	const (
		buildMeta = "v0.0.0+0c72fa6d4511"
		commit    = "0c72fa6d4511"
		canonical = "v1.7.2-0.20260603164357-0c72fa6d4511"
	)

	buildsrv, rec := newRecordingBuilder(t, defaultK6ModPath, true)
	// simulate go resolving the commit to its canonical pseudo-version
	rec.modVersions = map[string]string{defaultK6ModPath: canonical}

	artifact, err := buildsrv.Build(t.Context(), platform(), defaultK6ModPath, buildMeta, []k6build.Dependency{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := artifact.Dependencies["k6"]; got != canonical {
		t.Fatalf("expected k6 version %q in artifact, got %q", canonical, got)
	}

	if rec.k6Version != commit {
		t.Fatalf("expected foundry to receive k6 version %q, got %q", commit, rec.k6Version)
	}
}

// TestBuildPseudoVersionGatedByAllowBuildSemvers ensures that, like v0.0.0+<commit>
// build-metadata versions, pseudo-versions are rejected when AllowBuildSemvers is
// false (the default) since they build an arbitrary commit.
func TestBuildPseudoVersionGatedByAllowBuildSemvers(t *testing.T) {
	t.Parallel()

	const pseudo = "v1.7.2-0.20260603164357-0c72fa6d4511"

	buildsrv, _ := newRecordingBuilder(t, defaultK6ModPath, false)

	_, err := buildsrv.Build(t.Context(), platform(), defaultK6ModPath, pseudo, []k6build.Dependency{})
	if !errors.Is(err, k6build.ErrInvalidParameters) {
		t.Fatalf("expected ErrInvalidParameters, got %v", err)
	}
	if !errors.Is(err, ErrBuildSemverNotAllowed) {
		t.Fatalf("expected ErrBuildSemverNotAllowed, got %v", err)
	}
}
