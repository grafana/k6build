// Package store implements the object store server command
package store

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/store/file"
	"github.com/grafana/k6build/pkg/store/server"

	"github.com/spf13/cobra"
)

const (
	long = `
Starts a k6build objectstore server. 

The object server offers a REST API for storing and downloading objects.

Objects can be retrieved by a download url returned when the object is stored.

The --download-url specifies the base URL for downloading objects. This is necessary to allow
downloading the objects from different machines.
`

	example = `
# start the server serving an external url
k6build store --download-url http://external.url

# store object from same host
curl -x POST http://localhost:9000/store/objectID -d "object content" | jq .
{
	"Error": "",
	"Object": {
	  "ID": "objectID",
	  "Checksum": "17d3eb873fe4b1aac4f9d2505aefbb5b53b9a7f34a6aadd561be104c0e9d678b",
	  "URL": "http://external.url:9000/store/objectID/download"
	}
      }

# download object from another machine using the external url
curl http://external.url:9000/store/objectID/download
`
)

// New creates new cobra command for store command.
func New() *cobra.Command { //nolint:funlen
	var (
		storeDir        string
		storeSrvURL     string
		port            int
		logLevel        string
		shutdownTimeout time.Duration
	)

	cmd := &cobra.Command{
		Use:     "store",
		Short:   "k6build object store server",
		Long:    long,
		Example: example,
		// prevent the usage help to printed to stderr when an error is reported by a subcommand
		SilenceUsage: true,
		// this is needed to prevent cobra to print errors reported by subcommands in the stderr
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			// set log
			ll, err := k6build.ParseLogLevel(logLevel)
			if err != nil {
				return fmt.Errorf("parsing log level %w", err)
			}

			log := slog.New(
				slog.NewTextHandler(
					os.Stderr,
					&slog.HandlerOptions{
						Level: ll,
					},
				),
			)

			store, err := file.NewFileStore(storeDir)
			if err != nil {
				return fmt.Errorf("creating object store %w", err)
			}

			config := server.StoreServerConfig{
				BaseURL: storeSrvURL,
				Store:   store,
				Log:     log,
			}
			storeSrv, err := server.NewStoreServer(config)
			if err != nil {
				return fmt.Errorf("creating store server %w", err)
			}

			srv := http.NewServeMux()
			srv.Handle("/store/", storeSrv)

			httpServer := &http.Server{
				Addr:              fmt.Sprintf("0.0.0.0:%d", port),
				Handler:           srv,
				ReadHeaderTimeout: 5 * time.Second,
			}

			serverErrors := make(chan error, 1)

			go func() {
				log.Info("starting server", "address", httpServer.Addr, "object store", storeDir)
				err := httpServer.ListenAndServe()
				if err != nil && err != http.ErrServerClosed {
					serverErrors <- err
				}
			}()

			shutdown := make(chan os.Signal, 1)
			signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

			select {
			case err := <-serverErrors:
				return fmt.Errorf("server error: %w", err)

			case sig := <-shutdown:
				log.Debug("shutdown started", "signal", sig)

				ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
				defer cancel()

				if err := httpServer.Shutdown(ctx); err != nil {
					log.Error("graceful shutdown failed", "error", err)
					if err := httpServer.Close(); err != nil {
						return fmt.Errorf("could not stop server: %w", err)
					}
				}

				log.Debug("shutdown completed")
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&storeDir, "store-dir", "c", "/tmp/k6build/store", "object store directory")
	cmd.Flags().IntVarP(&port, "port", "p", 9000, "port server will listen")
	cmd.Flags().StringVarP(&storeSrvURL,
		"download-url", "d", "", "base url used for downloading objects."+
			"\nIf not specified http://localhost:<port> is used",
	)
	cmd.Flags().StringVarP(&logLevel, "log-level", "l", "INFO", "log level")
	cmd.Flags().DurationVar(
		&shutdownTimeout,
		"shutdown-timeout",
		10*time.Second,
		"maximum time to wait for graceful shutdown",
	)

	return cmd
}
