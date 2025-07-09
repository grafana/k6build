package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/api"
)

// process request and return a boolean indicating if request should be passed to the next handler in the chain
type requestHandler func(w http.ResponseWriter, r *http.Request) bool

func validateBuildRequest() requestHandler {
	return func(w http.ResponseWriter, r *http.Request) bool {
		req := api.BuildRequest{}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return false
		}

		if req.Platform == "" || req.K6Constrains == "" || len(req.Dependencies) == 0 {
			return false
		}

		return true
	}
}

func validateResolveRequest() requestHandler {
	return func(w http.ResponseWriter, r *http.Request) bool {
		req := api.ResolveRequest{}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return false
		}

		if req.K6Constrains == "" || len(req.Dependencies) == 0 {
			return false
		}

		return true
	}
}

func checkAuthorization(authType string, auth string) requestHandler {
	return func(w http.ResponseWriter, r *http.Request) bool {
		authHeader := fmt.Sprintf("%s %s", authType, auth)
		if r.Header.Get("Authorization") != authHeader {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
}

func checkHeader(headers map[string]string) requestHandler {
	return func(w http.ResponseWriter, r *http.Request) bool {
		for h, v := range headers {
			if r.Header.Get(h) != v {
				w.WriteHeader(http.StatusBadRequest)
				return false
			}
		}
		return true
	}
}

func response(status int, response any) requestHandler {
	return func(w http.ResponseWriter, _ *http.Request) bool {
		resp := &bytes.Buffer{}
		err := json.NewEncoder(resp).Encode(response)
		if err != nil {
			panic("unexpected error encoding response")
		}

		w.WriteHeader(status)
		_, _ = w.Write(resp.Bytes())

		return false
	}
}

// fail with the given error up to a number of times
func unreliable(status int, failures int) requestHandler {
	requests := 0
	return func(w http.ResponseWriter, r *http.Request) bool {
		requests++
		if requests <= failures {
			w.WriteHeader(status)
			return false
		}
		return true
	}
}

// creates a chain of handlers. Executes them until one returns false
func handlerChain(handlers ...requestHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		for _, handler := range handlers {
			if !handler(w, r) {
				return
			}
		}
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title     string
		headers   map[string]string
		auth      string
		authType  string
		handler   http.HandlerFunc
		expectErr error
	}{
		{
			title: "normal build",
			handler: handlerChain(
				validateBuildRequest(),
				response(http.StatusOK, api.BuildResponse{}),
			),
		},
		{
			title: "build request failed",
			handler: handlerChain(
				validateBuildRequest(),
				response(http.StatusOK, api.BuildResponse{Error: k6build.NewWrappedError(api.ErrBuildFailed, nil)}),
			),
			expectErr: api.ErrBuildFailed,
		},
		{
			title:    "auth header",
			auth:     "token",
			authType: "Bearer",
			handler: handlerChain(
				validateBuildRequest(),
				checkAuthorization("Bearer", "token"),
				response(http.StatusOK, api.BuildResponse{}),
			),
			expectErr: nil,
		},
		{
			title:    "with default auth type",
			auth:     "token",
			authType: "",
			handler: handlerChain(
				validateBuildRequest(),
				checkAuthorization("Bearer", "token"),
				response(http.StatusOK, api.BuildResponse{}),
			),
			expectErr: nil,
		},
		{
			title: "failed auth",
			handler: handlerChain(
				validateBuildRequest(),
				response(http.StatusUnauthorized, api.BuildResponse{Error: k6build.NewWrappedError(api.ErrRequestFailed, errors.New("unauthorized"))}),
			),
			expectErr: api.ErrRequestFailed,
		},
		{
			title: "custom headers",
			headers: map[string]string{
				"Custom-Header": "Custom-Value",
			},
			handler: handlerChain(
				validateBuildRequest(),
				checkHeader(map[string]string{"Custom-Header": "Custom-Value"}),
				response(http.StatusOK, api.BuildResponse{}),
			),
			expectErr: nil,
		},
		{
			title: "test retry request",
			handler: handlerChain(
				unreliable(http.StatusServiceUnavailable, 1),
				response(http.StatusOK, api.BuildResponse{}),
			),
			expectErr: nil,
		},
		{
			title: "test we don't retry forever",
			handler: handlerChain(
				unreliable(http.StatusServiceUnavailable, 10),
				response(http.StatusOK, api.BuildResponse{}),
			),
			expectErr: api.ErrRequestFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(tc.handler)

			defer srv.Close()

			client, err := NewBuildServiceClient(
				BuildServiceClientConfig{
					URL:               srv.URL,
					Headers:           tc.headers,
					Authorization:     tc.auth,
					AuthorizationType: tc.authType,
				},
			)
			if err != nil {
				t.Fatalf("unexpected %v", err)
			}

			_, err = client.Build(
				t.Context(),
				"linux/amd64",
				"v0.1.0",
				[]k6build.Dependency{{Name: "k6/x/test", Constraints: "*"}},
			)

			if !errors.Is(err, tc.expectErr) {
				t.Fatalf("expected %v got %v", tc.expectErr, err)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		title     string
		headers   map[string]string
		auth      string
		authType  string
		handler   http.HandlerFunc
		expectErr error
	}{
		{
			title: "normal build",
			handler: handlerChain(
				validateResolveRequest(),
				response(http.StatusOK, api.ResolveResponse{}),
			),
		},
		{
			title: "resolve request failed",
			handler: handlerChain(
				validateResolveRequest(),
				response(http.StatusOK, api.ResolveResponse{Error: k6build.NewWrappedError(api.ErrCannotSatisfy, nil)}),
			),
			expectErr: api.ErrCannotSatisfy,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(tc.handler)

			defer srv.Close()

			client, err := NewBuildServiceClient(
				BuildServiceClientConfig{
					URL:               srv.URL,
					Headers:           tc.headers,
					Authorization:     tc.auth,
					AuthorizationType: tc.authType,
				},
			)
			if err != nil {
				t.Fatalf("unexpected %v", err)
			}

			_, err = client.Resolve(
				context.TODO(),
				"v0.1.0",
				[]k6build.Dependency{{Name: "k6/x/test", Constraints: "*"}},
			)

			if !errors.Is(err, tc.expectErr) {
				t.Fatalf("expected %v got %v", tc.expectErr, err)
			}
		})
	}
}
