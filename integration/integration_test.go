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

	// 5. test building k6 with different options
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

			artifact, err := client.Build(context.TODO(), tc.platform, tc.k6Constrain, tc.deps)
			if err != nil {
				t.Fatalf("building artifact  %v", err)
			}

			k6BinPath := filepath.Join(t.TempDir(), k6BinaryName())
			err = util.Download(context.TODO(), artifact.URL, k6BinPath)
			if err != nil {
				t.Fatalf("building artifact  %v", err)
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
