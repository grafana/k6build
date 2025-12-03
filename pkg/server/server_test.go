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

func TestBuild(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title         string
		builder       k6build.BuildService
		req           any // use any to allow passing invalid requests values
		resp          *api.BuildResponse
		expectReponse any
		expectStatus  int
		expectErr     error
	}{
		{
			title: "build request",
			builder: mockBuilder{
				deps: map[string]string{"k6": "v0.1.0"},
			},
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
			req:          &api.BuildRequest{Platform: "linux/amd64", K6Constrains: "v0.1.0"},
			resp:         &api.BuildResponse{},
			expectStatus: http.StatusInternalServerError,
			expectErr:    api.ErrBuildFailed,
		},
		{
			title: "invalid build request (empty request object)",
			builder: mockBuilder{
				err: k6build.ErrInvalidParameters,
			},
			req:           &api.BuildRequest{},
			resp:          &api.BuildResponse{},
			expectReponse: &api.BuildResponse{},
			expectStatus:  http.StatusOK,
			expectErr:     api.ErrCannotSatisfy,
		},
		{
			title: "invalid build request (empty request body)",
			builder: mockBuilder{
				err: k6build.ErrInvalidParameters,
			},
			req:           nil,
			resp:          &api.BuildResponse{},
			expectReponse: &api.BuildResponse{},
			expectStatus:  http.StatusOK,
			expectErr:     api.ErrCannotSatisfy,
		},
		{
			title: "invalid build request (wrong struct)",
			builder: mockBuilder{
				deps: map[string]string{"k6": "v0.1.0"},
			},
			req:           struct{ Invalid string }{Invalid: "value"},
			resp:          &api.BuildResponse{},
			expectReponse: &api.BuildResponse{},
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
			resp, err := http.Post(url.JoinPath("build").String(), "application/json", req)
			if err != nil {
				t.Fatalf("making request %v", err)
			}
			defer func() {
				_ = resp.Body.Close()
			}()

			assertAPIResponse(t, resp, tc.expectStatus, tc.expectErr, tc.resp, tc.expectReponse)
		})
	}
}

func TestBuildGet(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title         string
		builder       k6build.BuildService
		params        map[string]string
		resp          *api.BuildResponse
		expectReponse any
		expectStatus  int
		expectErr     error
	}{
		{
			title: "build request",
			builder: mockBuilder{
				deps: map[string]string{"k6": "v0.1.0"},
			},
			params: map[string]string{"platform": "linux/amd64", "k6": "v0.1.0"},
			resp:   &api.BuildResponse{},
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
			params:       map[string]string{"platform": "linux/amd64", "k6": "v0.1.0"},
			resp:         &api.BuildResponse{},
			expectStatus: http.StatusInternalServerError,
			expectErr:    api.ErrBuildFailed,
		},
		{
			title: "missing required parameter (platform)",
			builder: mockBuilder{
				err: k6build.ErrInvalidParameters,
			},
			params:        map[string]string{},
			resp:          &api.BuildResponse{},
			expectReponse: &api.BuildResponse{},
			expectStatus:  http.StatusOK,
			expectErr:     api.ErrCannotSatisfy,
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

			u, _ := url.Parse(apiserver.URL)
			u = u.JoinPath("build")
			queryParams := url.Values{}
			for param, value := range tc.params {
				queryParams.Add(param, value)
			}

			resp, err := http.Get(u.String() + "?" + queryParams.Encode())
			if err != nil {
				t.Fatalf("making request %v", err)
			}
			defer func() {
				_ = resp.Body.Close()
			}()

			assertAPIResponse(t, resp, tc.expectStatus, tc.expectErr, tc.resp, tc.expectReponse)
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title         string
		builder       k6build.BuildService
		req           any // use any to allow passing invalid requests values
		resp          *api.ResolveResponse
		expectReponse any
		expectStatus  int
		expectErr     error
	}{
		{
			title: "resolve request",
			builder: mockBuilder{
				deps: map[string]string{"k6": "v0.1.0"},
			},
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
			req:           struct{ Invalid string }{Invalid: "value"},
			resp:          &api.ResolveResponse{},
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
			resp, err := http.Post(url.JoinPath("resolve").String(), "application/json", req)
			if err != nil {
				t.Fatalf("making request %v", err)
			}
			defer func() {
				_ = resp.Body.Close()
			}()

			assertAPIResponse(t, resp, tc.expectStatus, tc.expectErr, tc.resp, tc.expectReponse)
		})
	}
}

func assertAPIResponse(t *testing.T, r *http.Response, expectStatus int, expectErr error, actual any, expected any) {
	if r.StatusCode != expectStatus {
		t.Fatalf("expected status code: %d got %d", expectStatus, r.StatusCode)
	}

	// BadRequest do not return a meaningful response body
	if r.StatusCode == http.StatusBadRequest {
		return
	}

	err := json.NewDecoder(r.Body).Decode(&actual)
	if err != nil {
		t.Fatalf("decoding response %v", err)
	}

	// check Error in response, if any
	respErr := extractError(actual)
	if expectErr != nil && !errors.Is(respErr, expectErr) {
		t.Fatalf("expected error: %q got %q", expectErr, respErr)
	}

	// if error is expected, don't validate response
	if expectErr != nil {
		return
	}

	if !cmp.Equal(actual, expected) {
		t.Fatalf("%s", cmp.Diff(actual, expected))
		// t.Fatalf("expected %v got %v", tc.expectReponse, tc.resp)
	}
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
