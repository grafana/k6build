// Package server implements the build server command
package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/builder"
	"github.com/grafana/k6build/pkg/catalog"
	"github.com/grafana/k6build/pkg/server"
	"github.com/grafana/k6build/pkg/store"
	"github.com/grafana/k6build/pkg/store/client"
	"github.com/grafana/k6build/pkg/store/s3"

	"github.com/spf13/cobra"
)

const (
	long = `
Starts a k6build server

The server exposes an API for building custom k6 binaries.

The API returns the metadata of the custom binary, including an URL for downloading it,
but does not return the binary itself.

For example

	curl http://localhost:8000/build -d \
	'{
	  "k6":"v0.50.0",
	  "dependencies":[
	    {
		"name":"k6/x/kubernetes",
		"constraints":">v0.8.0"
	    }
	  ],
	  "platform":"linux/amd64"
	}' | jq .

	{
	  "artifact": {
	  "id": "5a241ba6ff643075caadbd06d5a326e5e74f6f10",
	  "url": "http://localhost:9000/store/5a241ba6ff643075caadbd06d5a326e5e74f6f10/download",
	  "dependencies": {
	    "k6": "v0.50.0",
	    "k6/x/kubernetes": "v0.10.0"
	  },
	  "platform": "linux/amd64",
	  "checksum": "bfdf51ec9279e6d7f91df0a342d0c90ab4990ff1fb0215938505a6894edaf913"
	  }
	}

Note: The build server disables CGO by default but enables it when a dependency requires it.
      use --enable-cgo=true to enable CGO support by default.
`

	example = `
# start the build server using a custom local catalog
k6build server -c /path/to/catalog.json

# start the build server using a custom GOPROXY
k6build server -e GOPROXY=http://localhost:80

# start the build server with a localstack s3 storage backend
# aws credentials are expected in the default location (e.g. env variables)
export AWS_ACCESS_KEY_ID="test"
export AWS_SECRET_ACCESS_KEY="test"
k6build server --s3-endpoint http://localhost:4566 --store-bucket k6build
`
)

// New creates new cobra command for the server command.
func New() *cobra.Command { //nolint:funlen
	var (
		allowBuildSemvers bool
		catalogURL        string
		copyGoEnv         bool
		enableCgo         bool
		goEnv             map[string]string
		logLevel          string
		port              int
		s3Bucket          string
		s3Endpoint        string
		s3Region          string
		storeURL          string
		verbose           bool
	)

	cmd := &cobra.Command{
		Use:     "server",
		Short:   "k6 build service",
		Long:    long,
		Example: example,
		// prevent the usage help to printed to stderr when an error is reported by a subcommand
		SilenceUsage: true,
		// this is needed to prevent cobra to print errors reported by subcommands in the stderr
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			catalog, err := catalog.NewCatalog(cmd.Context(), catalogURL)
			if err != nil {
				return fmt.Errorf("creating catalog %w", err)
			}

			var store store.ObjectStore

			if s3Bucket != "" {
				store, err = s3.New(s3.Config{
					Bucket:   s3Bucket,
					Endpoint: s3Endpoint,
					Region:   s3Region,
				})
				if err != nil {
					return fmt.Errorf("creating s3 store %w", err)
				}
			} else {
				store, err = client.NewStoreClient(client.StoreClientConfig{
					Server: storeURL,
				})
				if err != nil {
					return fmt.Errorf("creating store %w", err)
				}
			}

			// TODO: check this logic
			if enableCgo {
				log.Warn("enabling CGO for build service")
			} else {
				if goEnv == nil {
					goEnv = make(map[string]string)
				}
				goEnv["CGO_ENABLED"] = "0"
			}

			config := builder.Config{
				Opts: builder.Opts{
					GoOpts: builder.GoOpts{
						Env:       goEnv,
						CopyGoEnv: copyGoEnv,
					},
					Verbose:           verbose,
					AllowBuildSemvers: allowBuildSemvers,
				},
				Catalog: catalog,
				Store:   store,
			}
			buildSrv, err := builder.New(cmd.Context(), config)
			if err != nil {
				return fmt.Errorf("creating local build service  %w", err)
			}

			apiConfig := server.APIServerConfig{
				BuildService: buildSrv,
				Log:          log,
			}
			buildAPI := server.NewAPIServer(apiConfig)

			srv := http.NewServeMux()
			srv.Handle("POST /build", http.StripPrefix("/build", buildAPI))

			listerAddr := fmt.Sprintf("0.0.0.0:%d", port)
			log.Info("starting server", "address", listerAddr)
			err = http.ListenAndServe(listerAddr, srv) //nolint:gosec
			if err != nil {
				log.Info("server ended", "error", err.Error())
			}
			log.Info("ending server")

			return nil
		},
	}

	cmd.Flags().StringVarP(
		&catalogURL,
		"catalog",
		"c",
		catalog.DefaultCatalogURL,
		"dependencies catalog. Can be path to a local file or an URL."+
			"\n",
	)
	cmd.Flags().StringVar(&storeURL, "store-url", "http://localhost:9000/store", "store server url")
	cmd.Flags().StringVar(&s3Bucket, "store-bucket", "", "s3 bucket for storing binaries")
	cmd.Flags().StringVar(&s3Endpoint, "s3-endpoint", "", "s3 endpoint")
	cmd.Flags().StringVar(&s3Region, "s3-region", "", "aws region")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "print build process output")
	cmd.Flags().BoolVarP(&copyGoEnv, "copy-go-env", "g", true, "copy go environment")
	cmd.Flags().StringToStringVarP(&goEnv, "env", "e", nil, "build environment variables")
	cmd.Flags().IntVarP(&port, "port", "p", 8000, "port server will listen")
	cmd.Flags().StringVarP(&logLevel, "log-level", "l", "INFO", "log level")
	cmd.Flags().BoolVar(&enableCgo, "enable-cgo", false, "enable CGO for building binaries.")
	cmd.Flags().BoolVar(
		&allowBuildSemvers,
		"allow-build-semvers",
		false,
		"allow building versions with build metadata (e.g v0.0.0+build).",
	)

	return cmd
}
