// Package cmd contains build cobra command factory function.
package cmd

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/grafana/k6build/cmd/local"
	"github.com/grafana/k6build/cmd/remote"
	"github.com/grafana/k6build/cmd/server"
	"github.com/grafana/k6build/cmd/store"
)

// New creates a new root command for k6build
func New() *cobra.Command {
	root := &cobra.Command{
		Use:               "k6build",
		Short:             "Build custom k6 binaries with extensions",
		SilenceUsage:      true,
		SilenceErrors:     true,
		DisableAutoGenTag: true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		Version:           fullVersion(),
	}

	root.AddCommand(store.New())
	root.AddCommand(remote.New())
	root.AddCommand(local.New())
	root.AddCommand(server.New())
	root.AddCommand(newVersionCommand())

	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:           "version",
		Short:         "k6build version",
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root := cmd.Root()
			root.SetArgs([]string{"--version"})
			_ = root.Execute()
			return nil
		},
	}
}

const (
	commitKey      = "commit"
	commitDirtyKey = "commit_dirty"
)

// fullVersion returns the maximally full version and build information for
// the currently running k6build executable.
func fullVersion() string {
	details := versionDetails()

	goVersionArch := fmt.Sprintf("%s, %s/%s", details["go_version"], details["go_os"], details["go_arch"])

	k6buildversion := fmt.Sprintf("%s", details["version"])
	// for the fallback case when the version is not in the expected format
	// cobra adds a "v" prefix to the version
	k6buildversion = strings.TrimLeft(k6buildversion, "v")

	commit, ok := details[commitKey].(string)
	if !ok || commit == "" {
		return fmt.Sprintf("%s (%s)", k6buildversion, goVersionArch)
	}

	isDirty, ok := details[commitDirtyKey].(bool)
	if ok && isDirty {
		commit += "-dirty"
	}

	return fmt.Sprintf("%s (commit/%s, %s)", k6buildversion, commit, goVersionArch)
}

// versionDetails returns the structured details about version
func versionDetails() map[string]any {
	v := "unreleased"

	details := map[string]any{
		"version":    v,
		"go_version": runtime.Version(),
		"go_os":      runtime.GOOS,
		"go_arch":    runtime.GOARCH,
	}

	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return details
	}
	details["version"] = buildInfo.Main.Version

	var (
		commit string
		dirty  bool
	)
	for _, s := range buildInfo.Settings {
		switch s.Key {
		case "vcs.revision":
			commitLen := min(len(s.Value), 10)
			commit = s.Value[:commitLen]
		case "vcs.modified":
			if s.Value == "true" {
				dirty = true
			}
		default:
		}
	}

	if commit == "" {
		return details
	}

	details[commitKey] = commit
	if dirty {
		details[commitDirtyKey] = true
	}

	return details
}
