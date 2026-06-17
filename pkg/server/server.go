// Package server implements a build server
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/api"
)

// resolveK6ModPath returns the effective k6 module path, defaulting to K6ModPath (v1) when empty.
func resolveK6ModPath(k6ModPath string) string {
	if k6ModPath == "" {
		return k6build.K6ModPath
	}
	return k6ModPath
}

// APIServerConfig defines the configuration for the APIServer
type APIServerConfig struct {
	BuildService k6build.BuildService
	Log          *slog.Logger
	CacheMaxAge  time.Duration
}

// APIServer defines a k6build API server
type APIServer struct {
	srv         k6build.BuildService
	log         *slog.Logger
	cacheMaxAge time.Duration
}

// NewAPIServer creates a new build service API server
// TODO: add logger
func NewAPIServer(config APIServerConfig) http.Handler {
	log := config.Log
	if log == nil {
		log = slog.New(
			slog.NewTextHandler(
				io.Discard,
				&slog.HandlerOptions{},
			),
		)
	}

	server := &APIServer{
		srv:         config.BuildService,
		log:         log,
		cacheMaxAge: config.CacheMaxAge,
	}

	handler := http.NewServeMux()
	handler.HandleFunc("POST /build", server.BuildPost)
	handler.HandleFunc("GET /build", server.BuildGet)
	handler.HandleFunc("POST /resolve", server.Resolve)

	return handler
}

// BuildPost implements the request handler for the build request using post
func (a *APIServer) BuildPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json")

	req := api.BuildRequest{}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)

		resp := api.BuildResponse{}
		resp.Error = k6build.NewWrappedError(api.ErrInvalidRequest, err)
		_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson

		return
	}

	a.processBuildRequest(w, r, req)
}

// BuildGet implements the request handler for the build request using get
// the build arguments
// /build?k6=version&platform=version&dep=name:constrains&dep=name:constrains&nocache=true&k6modpath=go.k6.io/k6
func (a *APIServer) BuildGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json")

	req := api.BuildRequest{}
	req.Platform = r.URL.Query().Get("platform")
	req.K6Constrains = r.URL.Query().Get("k6")
	req.K6ModPath = r.URL.Query().Get("k6modpath")
	for _, dep := range r.URL.Query()["dep"] {
		name, constraints, _ := strings.Cut(dep, ":")
		req.Dependencies = append(req.Dependencies, k6build.Dependency{Name: name, Constraints: constraints})
	}
	req.NoCache = r.URL.Query().Get("nocache") == "true"

	a.processBuildRequest(w, r, req)
}

func (a *APIServer) processBuildRequest(w http.ResponseWriter, r *http.Request, req api.BuildRequest) {
	log := a.log.With(
		slog.String("platform", req.Platform),
		slog.String("k6", req.K6Constrains),
		slog.Any("dependencies", req.Dependencies),
	)

	ctx := r.Context()

	log.DebugContext(ctx, "processing", "request", req.String())

	resp := api.BuildResponse{}

	if req.NoCache {
		ctx = api.WithNoCache(ctx, true)
	}

	artifact, err := a.srv.Build(
		ctx,
		req.Platform,
		resolveK6ModPath(req.K6ModPath),
		req.K6Constrains,
		req.Dependencies,
	)

	switch {
	case err == nil:
		a.addCacheHeader(w, artifact)
		w.WriteHeader(http.StatusOK)
		resp.Artifact = artifact
		log.DebugContext(ctx, "returning", "response", resp.String())
	case errors.Is(err, k6build.ErrInvalidParameters):
		w.WriteHeader(http.StatusOK)
		resp.Error = k6build.NewWrappedError(api.ErrCannotSatisfy, err)
		log.InfoContext(ctx, resp.Error.Error())
	default:
		resp.Error = k6build.NewWrappedError(api.ErrBuildFailed, err)
		w.WriteHeader(http.StatusInternalServerError)
		log.ErrorContext(ctx, resp.Error.Error())
	}

	_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
}

func (a *APIServer) addCacheHeader(w http.ResponseWriter, artifact k6build.Artifact) {
	w.Header().Add("ETag", artifact.ID)
	if a.cacheMaxAge != 0 {
		maxAge := int(math.Trunc(a.cacheMaxAge.Seconds()))
		w.Header().Add("Cache-Control", fmt.Sprintf("max-age=%d", maxAge))
	}
}

// Resolve implements the request handler for the resolve request
func (a *APIServer) Resolve(w http.ResponseWriter, r *http.Request) {
	resp := api.ResolveResponse{}

	w.Header().Add("Content-Type", "application/json")

	req := api.ResolveRequest{}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		resp.Error = k6build.NewWrappedError(api.ErrInvalidRequest, err)
		return
	}

	log := a.log.With(
		slog.String("k6", req.K6Constrains),
		slog.Any("dependencies", req.Dependencies),
	)

	ctx := r.Context()

	log.DebugContext(ctx, "processing", "request", req.String())

	deps, err := a.srv.Resolve(
		ctx,
		resolveK6ModPath(req.K6ModPath),
		req.K6Constrains,
		req.Dependencies,
	)

	switch {
	case err == nil:
		resp.Dependencies = deps
		w.WriteHeader(http.StatusOK)
		log.DebugContext(ctx, "returning", "response", resp.String())
	case errors.Is(err, k6build.ErrInvalidParameters):
		w.WriteHeader(http.StatusOK)
		resp.Error = k6build.NewWrappedError(api.ErrCannotSatisfy, err)
		log.InfoContext(ctx, resp.Error.Error())
	default:
		resp.Error = k6build.NewWrappedError(api.ErrResolveFailed, err)
		w.WriteHeader(http.StatusInternalServerError)
		log.ErrorContext(ctx, resp.Error.Error())
	}

	_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
}
