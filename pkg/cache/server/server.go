// Package server implements a cache server
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/grafana/k6build/pkg/cache"
	"github.com/grafana/k6build/pkg/cache/api"
)

var ErrInvalidRequest = errors.New("invalid request") //nolint:revive

// CacheServer implements an http server that handles cache requests
type CacheServer struct {
	baseURL string
	cache   cache.Cache
	log     *slog.Logger
}

// CacheServerConfig defines the configuration for the APIServer
type CacheServerConfig struct {
	BaseURL string
	Cache   cache.Cache
	Log     *slog.Logger
}

// NewCacheServer returns a CacheServer backed by a cache
func NewCacheServer(config CacheServerConfig) http.Handler {
	log := config.Log

	if log == nil {
		log = slog.New(
			slog.NewTextHandler(
				io.Discard,
				&slog.HandlerOptions{},
			),
		)
	}
	cacheSrv := &CacheServer{
		baseURL: config.BaseURL,
		cache:   config.Cache,
		log:     log,
	}

	handler := http.NewServeMux()
	// FIXME: this should be PUT (used POST as http client doesn't have PUT method)
	handler.HandleFunc("POST /{id}", cacheSrv.Store)
	handler.HandleFunc("GET /{id}", cacheSrv.Get)
	handler.HandleFunc("GET /{id}/download", cacheSrv.Download)

	return handler
}

// Get retrieves an objects if exists in the cache or an error otherwise
func (s *CacheServer) Get(w http.ResponseWriter, r *http.Request) {
	resp := api.CacheResponse{}

	w.Header().Add("Content-Type", "application/json")

	id := r.PathValue("id")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		resp.Error = ErrInvalidRequest.Error()
		s.log.Error(resp.Error)
		_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
		return
	}

	object, err := s.cache.Get(context.Background(), id) //nolint:contextcheck
	if err != nil {
		if errors.Is(err, cache.ErrObjectNotFound) {
			s.log.Debug(err.Error())
			w.WriteHeader(http.StatusNotFound)
		} else {
			s.log.Error(err.Error())
			w.WriteHeader(http.StatusInternalServerError)
		}
		resp.Error = err.Error()
		_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson

		return
	}

	downloadURL := getDownloadURL(s.baseURL, r)
	resp.Object = cache.Object{
		ID:       id,
		Checksum: object.Checksum,
		URL:      downloadURL,
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
}

// Store stores the object and returns the metadata
func (s *CacheServer) Store(w http.ResponseWriter, r *http.Request) {
	resp := api.CacheResponse{}

	w.Header().Add("Content-Type", "application/json")

	// ensure errors are reported and logged
	defer func() {
		if resp.Error != "" {
			s.log.Error(resp.Error)
			_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
		}
	}()

	id := r.PathValue("id")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		resp.Error = ErrInvalidRequest.Error()
		return
	}

	object, err := s.cache.Store(context.Background(), id, r.Body) //nolint:contextcheck
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		resp.Error = err.Error()
		return
	}

	downloadURL := getDownloadURL(s.baseURL, r)
	resp.Object = cache.Object{
		ID:       id,
		Checksum: object.Checksum,
		URL:      downloadURL,
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
}

func getDownloadURL(baseURL string, r *http.Request) string {
	if baseURL != "" {
		return fmt.Sprintf("%s/%s/download", baseURL, r.PathValue("id"))
	}

	return fmt.Sprintf("http://%s%s/download", r.Host, r.RequestURI)
}

// Download returns an object's content given its id
func (s *CacheServer) Download(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	object, err := s.cache.Get(context.Background(), id) //nolint:contextcheck
	if err != nil {
		if errors.Is(err, cache.ErrObjectNotFound) {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}

	objectContent, err := s.cache.Download(context.Background(), object) //nolint:contextcheck
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer func() {
		_ = objectContent.Close()
	}()

	w.WriteHeader(http.StatusOK)
	w.Header().Add("Content-Type", "application/octet-stream")
	w.Header().Add("ETag", object.ID)
	_, _ = io.Copy(w, objectContent)
}
