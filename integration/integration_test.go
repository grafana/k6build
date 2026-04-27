//go:build integration
// +build integration

package k6provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/client"
	"github.com/grafana/k6build/pkg/testutils"
	"github.com/grafana/k6build/pkg/util"
)

func k6BinaryName() string {
	if runtime.GOOS == "windows" {
		return "k6.exe"
	}
	return "k6"
}

func Test_BuildServer(t *testing.T) {
	t.Parallel()

	testEnv, err := testutils.NewTestEnv(testutils.TestEnvConfig{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("test env setup %v", err)
	}
	t.Cleanup(testEnv.Cleanup)

	testCases := []struct {
		title       string
		platform    string
		k6Constrain string
		deps        []k6build.Dependency
	}{
		{
			title:       "build latest k6",
			platform:    fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
			k6Constrain: "*",
		},
	}

	for _, tc := range testCases { //nolint:paralleltest
		t.Run(tc.title, func(t *testing.T) {
			client, err := client.NewBuildServiceClient(
				client.BuildServiceClientConfig{
					URL: testEnv.BuildServiceURL(),
				},
			)
			if err != nil {
				t.Fatalf("client setup %v", err)
			}

			artifact, err := client.Build(context.TODO(), tc.platform, k6build.K6ModPath, tc.k6Constrain, tc.deps)
			if err != nil {
				t.Fatalf("building artifact  %v", err)
			}

			k6BinPath := filepath.Join(t.TempDir(), k6BinaryName())
			err = util.Download(context.TODO(), artifact.URL, k6BinPath)
			if err != nil {
				t.Fatalf("downloading artifact  %v", err)
			}

			info, err := os.Stat(k6BinPath)
			if err != nil {
				t.Fatalf("stat k6 %v", err)
			}
			if info.Size() == 0 {
				t.Fatalf("k6 binary is empty")
			}

			err = exec.Command(k6BinPath, "version").Run()
			if err != nil {
				t.Fatalf("running k6 %v", err)
			}
		})
	}
}

func Test_ConcurrentBuilds(t *testing.T) {
	t.Parallel()

	// Create a temporary directory to store the k6 binary.
	// This directory is shared by the store servers so ww can test conflicts accessing objects
	workDir := t.TempDir()

	testEnv1, err := testutils.NewTestEnv(testutils.TestEnvConfig{WorkDir: workDir})
	if err != nil {
		t.Fatalf("test env setup %v", err)
	}
	t.Cleanup(testEnv1.Cleanup)

	testEnv2, err := testutils.NewTestEnv(testutils.TestEnvConfig{WorkDir: workDir})
	if err != nil {
		t.Fatalf("test env setup %v", err)
	}
	t.Cleanup(testEnv2.Cleanup)

	platform := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	k6Constrain := "*"
	deps := []k6build.Dependency{}

	wg := sync.WaitGroup{}
	servers := []string{
		testEnv1.BuildServiceURL(),
		testEnv2.BuildServiceURL(),
		testEnv1.BuildServiceURL(),
		testEnv2.BuildServiceURL(),
	}
	errCh := make(chan error, len(servers))

	// start multiple concurrent builds to different servers
	for _, serverURL := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			client, err := client.NewBuildServiceClient(
				client.BuildServiceClientConfig{
					URL: serverURL,
				},
			)
			if err != nil {
				errCh <- fmt.Errorf("client setup %w", err)
				return
			}
			artifact, err := client.Build(context.TODO(), platform, k6build.K6ModPath, k6Constrain, deps)
			if err != nil {
				errCh <- fmt.Errorf("building artifact  %v", err)
				return
			}

			k6BinPath := filepath.Join(t.TempDir(), k6BinaryName())
			err = util.Download(context.TODO(), artifact.URL, k6BinPath)
			if err != nil {
				errCh <- fmt.Errorf("downloading artifact  %v", err)
				return
			}

			err = exec.Command(k6BinPath, "version").Run()
			if err != nil {
				errCh <- fmt.Errorf("running k6 %v", err)
				return
			}
		}()
	}

	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("concurrent builds %v", err)
	default:
	}
}

// Test_CatalogRouting verifies that k6build routes v1 and v2 build requests to the correct
// catalog based on the k6 version constraint. It uses Resolve (no binary build) so the test
// runs without requiring the full Go toolchain to download modules.
func Test_CatalogRouting(t *testing.T) {
	t.Parallel()

	// Use the testdata catalogs bundled with the builder package so we don't need network access.
	v1Catalog := "../pkg/builder/testdata/catalog-v1.json"
	v2Catalog := "../pkg/builder/testdata/catalog-v2.json"

	testEnv, err := testutils.NewTestEnv(testutils.TestEnvConfig{
		WorkDir:    t.TempDir(),
		CatalogURL: v1Catalog,
		CatalogsByModPath: map[string]string{
			"go.k6.io/k6/v2": v2Catalog,
		},
	})
	if err != nil {
		t.Fatalf("test env setup %v", err)
	}
	t.Cleanup(testEnv.Cleanup)

	buildClient, err := client.NewBuildServiceClient(client.BuildServiceClientConfig{
		URL: testEnv.BuildServiceURL(),
	})
	if err != nil {
		t.Fatalf("client setup %v", err)
	}

	testCases := []struct {
		title        string
		k6ModPath    string
		k6Constrains string
		wantK6       string
	}{
		{
			title:        "v1 mod path routes to v1 catalog",
			k6ModPath:    k6build.K6ModPath,
			k6Constrains: "*",
			wantK6:       "v0.57.0",
		},
		{
			title:        "v2 mod path routes to v2 catalog",
			k6ModPath:    "go.k6.io/k6/v2",
			k6Constrains: "*",
			wantK6:       "v2.0.0-rc1",
		},
		{
			title:        "empty mod path defaults to v1 catalog",
			k6ModPath:    "",
			k6Constrains: "*",
			wantK6:       "v0.57.0",
		},
	}

	for _, tc := range testCases { //nolint:paralleltest
		t.Run(tc.title, func(t *testing.T) {
			deps, err := buildClient.Resolve(context.TODO(), tc.k6ModPath, tc.k6Constrains, nil)
			if err != nil {
				t.Fatalf("resolving dependencies: %v", err)
			}
			if deps["k6"] != tc.wantK6 {
				t.Fatalf("expected k6 version %s, got %s", tc.wantK6, deps["k6"])
			}
		})
	}
}
