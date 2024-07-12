package k6build

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestAPIServer(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title  string
		req    BuildRequest
		expect BuildResponse
	}{
		{
			title: "build k6 v0.1.0 ",
			req: BuildRequest{
				Platform:     "linux/amd64",
				K6Constrains: "v0.1.0",
				Dependencies: []Dependency{},
			},
			expect: BuildResponse{
				Artifact: Artifact{
					Dependencies: map[string]string{"k6": "v0.1.0"},
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			buildsrv, err := SetupTestLocalBuildService(
				LocalBuildServiceConfig{
					CacheDir: t.TempDir(),
					Catalog:  filepath.Join("testdata", "catalog.json"),
				},
			)
			if err != nil {
				t.Fatalf("test setup %v", err)
			}

			config := APIServerConfig{
				BuildService: buildsrv,
			}
			apiserver := httptest.NewServer(NewAPIServer(config))

			req := bytes.Buffer{}
			err = json.NewEncoder(&req).Encode(&tc.req)
			if err != nil {
				t.Fatalf("test setup %v", err)
			}

			resp, err := http.Post(apiserver.URL, "application/json", &req)
			if err != nil {
				t.Fatalf("making request %v", err)
			}
			defer func() {
				_ = resp.Body.Close()
			}()

			buildResponse := BuildResponse{}
			err = json.NewDecoder(resp.Body).Decode(&buildResponse)
			if err != nil {
				t.Fatalf("decoding response %v", err)
			}

			if buildResponse.Error != tc.expect.Error {
				t.Fatalf("expected error: %s got %s", tc.expect.Error, buildResponse.Error)
			}

			// don't check artifact if error is expected
			if tc.expect.Error != "" {
				return
			}

			diff := cmp.Diff(
				tc.expect.Artifact.Dependencies,
				buildResponse.Artifact.Dependencies,
				cmpopts.SortSlices(dependencyComp))
			if diff != "" {
				t.Fatalf("dependencies don't match: %s\n", diff)
			}
		})
	}
}
