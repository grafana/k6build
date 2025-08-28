package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/api"
)

type mockBuilder struct {
	err  error
	deps map[string]string
}

func (m mockBuilder) Build(
	ctx context.Context,
	platform string,
	k6Constrains string,
	deps []k6build.Dependency,
) (k6build.Artifact, error) {
	if m.err != nil {
		return k6build.Artifact{}, m.err
	}

	return k6build.Artifact{
		Platform:     platform,
		Dependencies: m.deps,
	}, nil
}

func (m mockBuilder) Resolve(
	ctx context.Context,
	k6Constrains string,
	deps []k6build.Dependency,
) (map[string]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.deps, nil
}

// extracts the Error field from the struct s using reflection
// if the fields does not exist or is not of type error, returns nil
func extractError(s any) error {
	errField := reflect.ValueOf(s).Elem().FieldByName("Error")
	if errField.IsNil() {
		return nil
	}

	err, ok := errField.Interface().(error)
	if !ok {
		return nil
	}
	return err
}

// TestAPI tests the different endpoints exposed by the APIServer
// using generic request and response objects
func TestAPI(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title         string
		builder       k6build.BuildService
		path          string
		req           any // use any to allow passing invalid requests values
		resp          any // use this field to decode response. Must be a pointer to the expected response type
		expectReponse any
		expectStatus  int
		expectErr     error
	}{
		{
			title: "build request",
			builder: mockBuilder{
				deps: map[string]string{"k6": "v0.1.0"},
			},
			path: "build",
			req:  &api.BuildRequest{Platform: "linux/amd64", K6Constrains: "v0.1.0"},
			resp: &api.BuildResponse{},
			expectReponse: &api.BuildResponse{
				Artifact: k6build.Artifact{
					Platform:     "linux/amd64",
					Dependencies: map[string]string{"k6": "v0.1.0"},
				},
			},
			expectStatus: http.StatusOK,
			expectErr:    nil,
		},
		{
			title: "build error",
			builder: mockBuilder{
				err: k6build.ErrBuildFailed,
			},
			path:         "build",
			req:          &api.BuildRequest{Platform: "linux/amd64", K6Constrains: "v0.1.0"},
			resp:         &api.BuildResponse{},
			expectStatus: http.StatusInternalServerError,
			expectErr:    api.ErrBuildFailed,
		},
		{
			title: "invalid build request (empty request object)",
			builder: mockBuilder{
				deps: map[string]string{"k6": "v0.1.0"},
			},
			path:          "build",
			req:           "",
			expectReponse: &api.BuildResponse{},
			expectStatus:  http.StatusBadRequest,
			expectErr:     nil,
		},
		{
			title: "invalid build request (wrong struct)",
			builder: mockBuilder{
				deps: map[string]string{"k6": "v0.1.0"},
			},
			path:          "build",
			req:           struct{ Invalid string }{Invalid: "value"},
			expectReponse: &api.BuildResponse{},
			expectStatus:  http.StatusBadRequest,
			expectErr:     nil,
		},
		{
			title: "resolve request",
			builder: mockBuilder{
				deps: map[string]string{"k6": "v0.1.0"},
			},
			path: "resolve",
			req:  &api.ResolveRequest{K6Constrains: "v0.1.0"},
			resp: &api.ResolveResponse{},
			expectReponse: &api.ResolveResponse{
				Dependencies: map[string]string{"k6": "v0.1.0"},
			},
			expectStatus: http.StatusOK,
			expectErr:    nil,
		},
		{
			title: "resolve error",
			builder: mockBuilder{
				err: k6build.ErrInvalidParameters,
			},
			path:         "resolve",
			req:          &api.ResolveRequest{K6Constrains: "v0.1.0"},
			resp:         &api.ResolveResponse{},
			expectStatus: http.StatusOK,
			expectErr:    api.ErrCannotSatisfy,
		},
		{
			title: "invalid resolve request (empty request object)",
			builder: mockBuilder{
				deps: map[string]string{"k6": "v0.1.0"},
			},
			path:         "resolve",
			req:          "",
			resp:         &api.ResolveResponse{},
			expectStatus: http.StatusBadRequest,
			expectErr:    nil,
		},
		{
			title: "invalid resolve request (wrong struct)",
			builder: mockBuilder{
				deps: map[string]string{"k6": "v0.1.0"},
			},
			path:          "resolve",
			req:           struct{ Invalid string }{Invalid: "value"},
			expectReponse: &api.ResolveResponse{},
			expectStatus:  http.StatusBadRequest,
			expectErr:     nil,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			config := APIServerConfig{
				BuildService: tc.builder,
			}
			apiserver := httptest.NewServer(NewAPIServer(config))

			req := &bytes.Buffer{}
			err := json.NewEncoder(req).Encode(tc.req)
			if err != nil {
				t.Fatalf("encoding request %v", err)
			}

			url, _ := url.Parse(apiserver.URL)
			resp, err := http.Post(url.JoinPath(tc.path).String(), "application/json", req)
			if err != nil {
				t.Fatalf("making request %v", err)
			}
			defer func() {
				_ = resp.Body.Close()
			}()

			if resp.StatusCode != tc.expectStatus {
				t.Fatalf("expected status code: %d got %d", tc.expectStatus, resp.StatusCode)
			}

			// if non 200 response is expected, don't validate response
			if tc.expectStatus != http.StatusOK {
				return
			}

			err = json.NewDecoder(resp.Body).Decode(&tc.resp)
			if err != nil {
				t.Fatalf("decoding response %v", err)
			}

			// check Error in response, if any
			respErr := extractError(tc.resp)
			if tc.expectErr != nil && !errors.Is(respErr, tc.expectErr) {
				t.Fatalf("expected error: %q got %q", tc.expectErr, respErr)
			}

			// if error is expected, don't validate response
			if tc.expectErr != nil {
				return
			}

			if !cmp.Equal(tc.resp, tc.expectReponse) {
				t.Fatalf("%s", cmp.Diff(tc.resp, tc.expectReponse))
				// t.Fatalf("expected %v got %v", tc.expectReponse, tc.resp)
			}
		})
	}
}
