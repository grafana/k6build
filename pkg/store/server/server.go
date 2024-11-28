// Package server implements an object store server
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/store"
	"github.com/grafana/k6build/pkg/store/api"
)

// StoreServer implements an http server that handles object store requests
type StoreServer struct {
	baseURL string
	store   store.ObjectStore
	log     *slog.Logger
}

// StoreServerConfig defines the configuration for the APIServer
type StoreServerConfig struct {
	BaseURL string
	Store   store.ObjectStore
	Log     *slog.Logger
}

// NewStoreServer returns a StoreServer backed by a file object store
func NewStoreServer(config StoreServerConfig) http.Handler {
	log := config.Log

	if log == nil {
		log = slog.New(
			slog.NewTextHandler(
				io.Discard,
				&slog.HandlerOptions{},
			),
		)
	}
	storeSrv := &StoreServer{
		baseURL: config.BaseURL,
		store:   config.Store,
		log:     log,
	}

	handler := http.NewServeMux()
	// FIXME: this should be PUT (used POST as http client doesn't have PUT method)
	handler.HandleFunc("POST /{id}", storeSrv.Store)
	handler.HandleFunc("GET /{id}", storeSrv.Get)
	handler.HandleFunc("GET /{id}/download", storeSrv.Download)

	return handler
}

// Get retrieves an objects if exists in the object store or an error otherwise
func (s *StoreServer) Get(w http.ResponseWriter, r *http.Request) {
	resp := api.StoreResponse{}

	w.Header().Add("Content-Type", "application/json")

	id := r.PathValue("id")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		resp.Error = k6build.NewWrappedError(api.ErrInvalidRequest, fmt.Errorf("object id is required"))
		s.log.Error(resp.Error.Error())
		_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
		return
	}

	object, err := s.store.Get(context.Background(), id) //nolint:contextcheck
	if err != nil {
		if errors.Is(err, store.ErrObjectNotFound) {
			s.log.Debug(err.Error())
			w.WriteHeader(http.StatusNotFound)
		} else {
			s.log.Error(err.Error())
			w.WriteHeader(http.StatusInternalServerError)
		}
		resp.Error = k6build.NewWrappedError(api.ErrObjectStoreAccess, err)
		_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson

		return
	}

	downloadURL := getDownloadURL(s.baseURL, r)
	resp.Object = store.Object{
		ID:       id,
		Checksum: object.Checksum,
		URL:      downloadURL,
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
}

// Store stores the object and returns the metadata
func (s *StoreServer) Store(w http.ResponseWriter, r *http.Request) {
	resp := api.StoreResponse{}

	w.Header().Add("Content-Type", "application/json")

	// ensure errors are reported and logged
	defer func() {
		if resp.Error != nil {
			s.log.Error(resp.Error.Error())
			_ = json.NewEncoder(w).Encode(resp) //nolint:errchkjson
		}
	}()

	id := r.PathValue("id")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		resp.Error = k6build.NewWrappedError(api.ErrInvalidRequest, fmt.Errorf("object id is required"))
		return
	}

	object, err := s.store.Put(context.Background(), id, r.Body) //nolint:contextcheck
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		resp.Error = k6build.NewWrappedError(api.ErrObjectStoreAccess, err)
		return
	}

	downloadURL := getDownloadURL(s.baseURL, r)
	resp.Object = store.Object{
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
func (s *StoreServer) Download(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	object, err := s.store.Get(context.Background(), id) //nolint:contextcheck
	if err != nil {
		if errors.Is(err, store.ErrObjectNotFound) {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}

	objectContent, err := s.store.Download(context.Background(), object) //nolint:contextcheck
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