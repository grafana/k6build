// Package k6build defines a service for building k6 binaries
package k6build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

const (
	// K6ModPath is the Go module path for k6. Used as the default when k6_mod_path is empty.
	K6ModPath = "go.k6.io/k6"
)

var (
	ErrAccessingArtifact     = errors.New("accessing artifact") //nolint:revive
	ErrBuildFailed           = errors.New("build failed")
	ErrCatalog               = errors.New("accessing catalog")
	ErrInvalidParameters     = errors.New("invalid build parameters")
	ErrResolvingDependencies = errors.New("resolving dependencies")
)

// Dependency defines a dependency and its semantic version constrains
type Dependency struct {
	// Name is the name of the dependency.
	Name string `json:"name,omitempty"`
	// Constraints specifies the semantic version constraints. E.g. >v0.2.0
	Constraints string `json:"constraints,omitempty"`
}

// Artifact defines the metadata of binary that satisfies a set of dependencies
// including a URL for downloading it.
type Artifact struct {
	// Unique id. Binaries satisfying the same set of dependencies have the same ID
	ID string `json:"id,omitempty"`
	// URL to fetch the artifact's binary
	URL string `json:"url,omitempty"`
	// List of dependencies that the artifact provides
	Dependencies map[string]string `json:"dependencies,omitempty"`
	// platform
	Platform string `json:"platform,omitempty"`
	// binary checksum (sha256)
	Checksum string `json:"checksum,omitempty"`
	// K6ModPath is the Go module path of the k6 core used in the build
	// (e.g. "go.k6.io/k6" or "go.k6.io/k6/v2").
	K6ModPath string `json:"k6_mod_path,omitempty"`
	// Warnings contains non-fatal issues detected during the build, such as
	// an extension depending on a different k6 major version than the one built.
	Warnings []string `json:"warnings,omitempty"`
}

// String returns a text serialization of the Artifact
func (a Artifact) String() string {
	return a.toString(true, " ")
}

// Print returns a string with a pretty print of the artifact
func (a Artifact) Print() string {
	return a.toString(true, "\n")
}

// PrintSummary returns a string with a pretty print of the artifact
func (a Artifact) PrintSummary() string {
	return a.toString(false, "\n")
}

// Print returns a text serialization of the Artifact
func (a Artifact) toString(details bool, sep string) string {
	buffer := &bytes.Buffer{}
	if details {
		fmt.Fprintf(buffer, "id: %s%s", a.ID, sep)
	}
	fmt.Fprintf(buffer, "platform: %s%s", a.Platform, sep)
	for dep, version := range a.Dependencies {
		fmt.Fprintf(buffer, "%s:%q%s", dep, version, sep)
	}
	fmt.Fprintf(buffer, "checksum: %s%s", a.Checksum, sep)
	if details {
		fmt.Fprintf(buffer, "url: %s%s", a.URL, sep)
	}
	return buffer.String()
}

// BuildService defines the interface for building custom k6 binaries
type BuildService interface {
	// Build returns a k6 Artifact that satisfies a set dependencies and version constrains.
	// k6ModPath is the Go module path for the k6 core (e.g. "go.k6.io/k6" or "go.k6.io/k6/v2").
	// An empty k6ModPath defaults to K6ModPath.
	Build(ctx context.Context, platform string, k6ModPath string, k6Constrains string, deps []Dependency) (Artifact, error)

	// Resolve returns the versions that satisfy the given dependency constrains or an error if they
	// cannot be satisfied.
	// k6ModPath is the Go module path for the k6 core (e.g. "go.k6.io/k6" or "go.k6.io/k6/v2").
	// An empty k6ModPath defaults to K6ModPath.
	Resolve(ctx context.Context, k6ModPath string, k6Constrains string, deps []Dependency) (map[string]string, error)
}
