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
// /build?k6=version&platform=version&dep=name:constrains&dep=name:constrains
func (a *APIServer) BuildGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json")

	req := api.BuildRequest{}
	req.Platform = r.URL.Query().Get("platform")
	req.K6Constrains = r.URL.Query().Get("k6")
	for _, dep := range r.URL.Query()["dep"] {
		name, constraints, _ := strings.Cut(dep, ":")
		req.Dependencies = append(req.Dependencies, k6build.Dependency{Name: name, Constraints: constraints})
	}

	a.processBuildRequest(w, r, req)
}

func (a *APIServer) processBuildRequest(w http.ResponseWriter, r *http.Request, req api.BuildRequest) {
	a.log.Debug("processing", "request", req.String())

	resp := api.BuildResponse{}

	artifact, err := a.srv.Build(
		r.Context(),
		req.Platform,
		req.K6Constrains,
		req.Dependencies,
	)

	switch {
	case err == nil:
		a.addCacheHeader(w, artifact)
		w.WriteHeader(http.StatusOK)
		resp.Artifact = artifact
		a.log.Debug("returning", "response", resp.String())
	case errors.Is(err, k6build.ErrInvalidParameters):
		w.WriteHeader(http.StatusOK)
		resp.Error = k6build.NewWrappedError(api.ErrCannotSatisfy, err)
		a.log.Info(resp.Error.Error())
	default:
		resp.Error = k6build.NewWrappedError(api.ErrBuildFailed, err)
		w.WriteHeader(http.StatusInternalServerError)
		a.log.Error(resp.Error.Error())
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

	a.log.Debug("processing", "request", req.String())

	deps, err := a.srv.Resolve(
		r.Context(),
		req.K6Constrains,
		req.Dependencies,
	)

	switch {
	case err == nil:
		resp.Dependencies = deps
		w.WriteHeader(http.StatusOK)
		a.log.Debug("returning", "response", resp.String())
	case errors.Is(err, k6build.ErrInvalidParameters):
		w.WriteHeader(http.StatusOK)
		resp.Error = k6build.NewWrappedError(api.ErrCannotSatisfy, err)
		a.log.Info(resp.Error.Error())
	default:
		resp.Error = k6build.NewWrappedError(api.ErrResolveFailed, err)
		w.WriteHeader(http.StatusInternalServerError)
		a.log.Error(resp.Error.Error())
	}

	_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
}
