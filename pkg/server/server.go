// Package server implements a build server
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/api"
)

// APIServerConfig defines the configuration for the APIServer
type APIServerConfig struct {
	BuildService k6build.BuildService
	Log          *slog.Logger
}

// APIServer defines a k6build API server
type APIServer struct {
	srv k6build.BuildService
	log *slog.Logger
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
		srv: config.BuildService,
		log: log,
	}

	handler := http.NewServeMux()
	handler.HandleFunc("POST /build", server.Build)
	handler.HandleFunc("POST /resolve", server.Resolve)

	return handler
}

// Build implements the request handler for the build request
func (a *APIServer) Build(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json")

	resp := api.BuildResponse{}

	// ensure errors are reported and logged
	defer func() {
		switch {
		case resp.Error == nil:
			return
		case errors.Is(resp.Error, k6build.ErrInvalidParameters):
			a.log.Info(resp.Error.Error())
		default:
			a.log.Error(resp.Error.Error())
		}
		_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
	}()

	req := api.BuildRequest{}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		resp.Error = k6build.NewWrappedError(api.ErrInvalidRequest, err)
		return
	}

	a.log.Debug("processing", "request", req.String())

	artifact, err := a.srv.Build( //nolint:contextcheck
		context.Background(),
		req.Platform,
		req.K6Constrains,
		req.Dependencies,
	)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		resp.Error = k6build.NewWrappedError(api.ErrBuildFailed, err)
		return
	}

	resp.Artifact = artifact

	a.log.Debug("returning", "response", resp.String())

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
}

// Resolve implements the request handler for the resolve request
func (a *APIServer) Resolve(w http.ResponseWriter, r *http.Request) {
	resp := api.ResolveResponse{}

	w.Header().Add("Content-Type", "application/json")

	// ensure errors are reported and logged
	defer func() {
		switch {
		case resp.Error == nil:
			return
		case errors.Is(resp.Error, k6build.ErrInvalidParameters):
			a.log.Info(resp.Error.Error())
		default:
			a.log.Error(resp.Error.Error())
		}
		_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
	}()

	req := api.ResolveRequest{}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	// err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		resp.Error = k6build.NewWrappedError(api.ErrInvalidRequest, err)
		return
	}

	a.log.Debug("processing", "request", req.String())

	deps, err := a.srv.Resolve( //nolint:contextcheck
		context.Background(),
		req.K6Constrains,
		req.Dependencies,
	)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		resp.Error = k6build.NewWrappedError(api.ErrResolveFailed, err)
		return
	}

	a.log.Debug("returning", "response", resp.String())

	resp.Dependencies = deps
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
}
