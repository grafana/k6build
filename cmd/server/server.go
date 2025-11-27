// Package server implements the build server command
package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/grafana/k6build"
	"github.com/grafana/k6build/pkg/builder"
	"github.com/grafana/k6build/pkg/catalog"
	"github.com/grafana/k6build/pkg/httpserver"
	"github.com/grafana/k6build/pkg/lock"
	"github.com/grafana/k6build/pkg/server"
	"github.com/grafana/k6build/pkg/store"
	"github.com/grafana/k6build/pkg/store/client"
	"github.com/grafana/k6build/pkg/store/s3"
	"github.com/grafana/k6build/pkg/util"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spf13/cobra"
)

const (
	long = `
Starts a k6build server

API
---

The server exposes an API for building custom k6 binaries.

Build
=====

The build endpoint returns the metadata of the custom binary, including an URL for downloading it,
but does not return the binary itself.

This function is implemented in the /build endpoint, and supports both POST and GET methods.

POST

The POST method expects the build request in the request's body as a JSON object.

Example

	curl -X POST http://localhost:8000/build -d \
	'{
	  "k6":"v1.4.0",
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
	    "k6": "v1.4.0",
	    "k6/x/kubernetes": "v0.10.0"
	  },
	  "platform": "linux/amd64",
	  "checksum": "bfdf51ec9279e6d7f91df0a342d0c90ab4990ff1fb0215938505a6894edaf913"
	  }
	}

GET

The GET method expects the build requests in the query parameters:
- platform: platform to build the binary for (e.g. linux/arm64). This is required
- k6: the k6 version constrains (e.g. k6=v1.2.0)
- dep: a dependency in the form name:version (e.g. dep=k6/x/faker:v0.4.0).
  Multple dependencies can be defined in a request.

Example

	curl -X GET "http://localhost:8000/build?platform=linux/amd64&k6=v1.4.0&dep=k6/x/kubernetes:>v0.8.0" | jq .

	{
	  "artifact": {
	  "id": "5a241ba6ff643075caadbd06d5a326e5e74f6f10",
	  "url": "http://localhost:9000/store/5a241ba6ff643075caadbd06d5a326e5e74f6f10/download",
	  "dependencies": {
	    "k6": "v1.4.0",
	    "k6/x/kubernetes": "v0.10.0"
	  },
	  "platform": "linux/amd64",
	  "checksum": "bfdf51ec9279e6d7f91df0a342d0c90ab4990ff1fb0215938505a6894edaf913"
	  }
	}

Caching

The GET method can be used to allow caching the build results. This method returns the ETag header with the
artifact's ID and sets the Cache-Control max-age parameter to the value specified in the --cache-max-age argument.

The ETag will be the same for the same dependencies, regardless of the order of the parameters, but some caching proxies
rely on the URL. Therefore, to improve cacheability, the parameters should be in a consistent order. We recommend 
placing first platform, then k6 (if present) and finally the deps in alphabetical order.
Example: plarform=linux/arm64&k6=v1.2.0&dep=k6/x/faker:v0.4.0&dep=k6/x/kafka:*

Notice that if the requests uses contrains like "*" or ">vx.y.z" for k6 or the dependencies, each build request 
can potentially return a different artifact if a new version is released. Therefore we recommend not setting the
max-age to a large value (days) and use instead in the range of a few hours to one day, dependending on how critical
is to use new versions.

Resolve
=======

The Resolve operation returns the versions that satisfy the given dependency constrains or
an error if they cannot be satisfied.

For example

	curl http://localhost:8000/resolve -d \
	'{
	  "k6":"v0.50.0",
	  "dependencies":[
	    {
		"name":"k6/x/kubernetes",
		"constraints":">v0.8.0"
	    }
	  ],
	}' | jq .

	{
	  "dependencies": {
	    "k6": "v0.50.0",
	    "k6/x/kubernetes": "v0.10.0"
	  },
	}

Metrics
--------

The server exposes prometheus metrics at /metrics.

requests_total           The total number of builds requests (counter)
request_duration_seconds duration of the build request in seconds (histogram)
                         Buckets: 0.1, 0.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 300
object_store_hits_total  The total number of build requests served from object store (counter)
builds_total             The total number of builds
builds_failed_total      The total number of failed builds (counter)
builds_invalid_total     The total number of builds with invalid parameters (counter).
                         Includes extension/versions not available and platforms not supported.
build_duration_seconds   The duration of the build in seconds (histogram)
                         Buckets: 1, 5, 10, 20, 30, 45, 60, 75, 90, 105, 120, 300

Liveness Probe
--------------

The server exposes a liveness check at /alive.
This endpoint returns a response code 200 with an empty body.

Build Lock
----------

Building a binary is a resource intensive task. The build server uses a lock to prevent 
concurrent builds of the same binary. By default, it uses a lock that works locally for 
a build service.

The --build-lock option allows to select a s3-backed global lock that works across
instances. This lock works by creating a lock object in a s3 bucket, using the conditional
put to prevent multiple instances creating it. The one creating the lock, holds it. Those that
failed to acquire the lock will retry periodically until they acquire it.

To ensure the liveness of the process, the owner must uptade its lease of the lock frequently
(this is done automatically by the lock implementation). If it is fails to do so, after a grace
period, the lock is released. Also, there's a maximum time it can hold the lock, even if it keeps
updating it. The s3-lock-* parameters allows to fine-tune this process.

Note: There are no guarantees the global lock will prevent concurrent builds, but it lowers the
probability of this happing. Given that building the binary is an indenpontent operation, this is
poses not risk.
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

type serverConfig struct {
	allowBuildSemvers bool
	catalogURL        string
	copyGoEnv         bool
	enableCgo         bool
	goEnv             map[string]string
	port              int
	buildLock         string
	s3Bucket          string
	s3Endpoint        string
	s3Region          string
	s3LockLease       time.Duration
	s3LockBackoff     time.Duration
	s3LockGrace       time.Duration
	s3LockMaxLease    time.Duration
	storeURL          string
	verbose           bool
	shutdownTimeout   time.Duration
	cacheMaxAge       time.Duration
}

// New creates new cobra command for the server command.
func New() *cobra.Command { //nolint:funlen
	var (
		cfg      = serverConfig{}
		logLevel string
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
			log, err := getLogger(logLevel)
			if err != nil {
				return err
			}

			if cfg.enableCgo {
				log.Warn("CGO is enabled by default. Use --enable-cgo=false to disable it.")
			}

			buildSrv, err := cfg.getBuildService(cmd.Context())
			if err != nil {
				return err
			}

			apiConfig := server.APIServerConfig{
				BuildService: buildSrv,
				Log:          log,
				CacheMaxAge:  cfg.cacheMaxAge,
			}
			buildServer := server.NewAPIServer(apiConfig)

			srvConfig := httpserver.ServerConfig{
				Logger:            log,
				Port:              cfg.port,
				EnableMetrics:     true,
				LivenessProbe:     true,
				ReadHeaderTimeout: 5 * time.Second,
			}

			srv := httpserver.NewServer(srvConfig)
			srv.Handle("/", buildServer)

			err = srv.Start(cmd.Context())
			if err != nil {
				return fmt.Errorf("error serving requests %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(
		&cfg.catalogURL,
		"catalog",
		"c",
		catalog.DefaultCatalogURL,
		"dependencies catalog. Can be path to a local file or an URL.",
	)
	cmd.Flags().StringVar(
		&cfg.storeURL,
		"store-url",
		"http://localhost:9000",
		"store server url",
	)
	cmd.Flags().StringVar(&cfg.s3Bucket, "store-bucket", "", "deprecated. Use s3-bucket")
	cmd.Flags().StringVar(&cfg.s3Bucket, "s3-bucket", "", "s3 bucket for storing binaries")
	cmd.Flags().StringVar(&cfg.s3Endpoint, "s3-endpoint", "", "s3 endpoint")
	cmd.Flags().StringVar(&cfg.s3Region, "s3-region", "", "aws region")
	cmd.Flags().StringVar(
		&cfg.buildLock,
		"build-lock",
		"local",
		"lock to prevent concurrent builds: 'local' or 's3' (across instances)",
	)
	cmd.Flags().BoolVarP(&cfg.verbose, "verbose", "v", false, "print build process output")
	cmd.Flags().BoolVarP(&cfg.copyGoEnv, "copy-go-env", "g", true, "copy go environment")
	cmd.Flags().StringToStringVarP(&cfg.goEnv, "env", "e", nil, "build environment variables")
	cmd.Flags().IntVarP(&cfg.port, "port", "p", 8000, "port server will listen")
	cmd.Flags().StringVarP(&logLevel, "log-level", "l", "INFO", "log level")
	cmd.Flags().BoolVar(&cfg.enableCgo, "enable-cgo", false, "enable CGO for building binaries.")
	cmd.Flags().BoolVar(
		&cfg.allowBuildSemvers,
		"allow-build-semvers",
		false,
		"allow building versions with build metadata (e.g v0.0.0+build).",
	)
	cmd.Flags().DurationVar(
		&cfg.shutdownTimeout,
		"shutdown-timeout",
		10*time.Second,
		"maximum time to wait for graceful shutdown",
	)
	cmd.Flags().DurationVar(
		&cfg.cacheMaxAge,
		"cache-max-age",
		0,
		"chache max-time for artifacts",
	)
	cmd.Flags().DurationVar(
		&cfg.s3LockLease,
		"s3-lock-lease",
		time.Second,
		"time the lock is granted to the owner. The owner should renew the lock at least"+
			" once before the lease expires.",
	)
	cmd.Flags().DurationVar(
		&cfg.s3LockGrace,
		"s3-lock-grace",
		3*time.Second,
		"grace period for renewing the lease. If the lock has not been updated before this"+
			" time, it is considered expired",
	)
	cmd.Flags().DurationVar(
		&cfg.s3LockMaxLease,
		"s3-lock-max-lease",
		3*time.Second,
		"the maximum time a lock can be held. After this time, it is automatically released",
	)
	cmd.Flags().DurationVar(
		&cfg.s3LockBackoff,
		"s3-lock-backoff",
		time.Second,
		"time between retries for acquiring a lock",
	)

	return cmd
}

func getLogger(logLevel string) (*slog.Logger, error) {
	ll, err := util.ParseLogLevel(logLevel)
	if err != nil {
		return nil, fmt.Errorf("parsing log level %w", err)
	}

	return slog.New(
		slog.NewTextHandler(
			os.Stderr,
			&slog.HandlerOptions{
				Level: ll,
			},
		),
	), nil
}

func (cfg serverConfig) getBuildService(ctx context.Context) (k6build.BuildService, error) {
	store, err := cfg.getStore() //nolint:contextcheck
	if err != nil {
		return nil, err
	}

	lock, err := cfg.getLock() //nolint:contextcheck // false positive no context required
	if err != nil {
		return nil, err
	}

	if cfg.goEnv == nil {
		cfg.goEnv = make(map[string]string)
	}
	cgoEnabled := "0"
	if cfg.enableCgo {
		cgoEnabled = "1"
	}
	cfg.goEnv["CGO_ENABLED"] = cgoEnabled

	config := builder.Config{
		Opts: builder.Opts{
			GoOpts: builder.GoOpts{
				Env:       cfg.goEnv,
				CopyGoEnv: cfg.copyGoEnv,
			},
			Verbose:           cfg.verbose,
			AllowBuildSemvers: cfg.allowBuildSemvers,
		},
		Catalog:    cfg.catalogURL,
		Store:      store,
		Registerer: prometheus.DefaultRegisterer,
		Lock:       lock,
	}
	builder, err := builder.New(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("creating local build service  %w", err)
	}

	return builder, nil
}

func (cfg serverConfig) getStore() (store.ObjectStore, error) {
	var (
		err   error
		store store.ObjectStore
	)

	if cfg.s3Bucket != "" {
		store, err = s3.New(s3.Config{
			Bucket:   cfg.s3Bucket,
			Endpoint: cfg.s3Endpoint,
			Region:   cfg.s3Region,
		})
		if err != nil {
			return nil, fmt.Errorf("creating s3 store %w", err)
		}
	} else {
		store, err = client.NewStoreClient(client.StoreClientConfig{
			Server: cfg.storeURL,
		})
		if err != nil {
			return nil, fmt.Errorf("creating store %w", err)
		}
	}

	return store, nil
}

func (cfg serverConfig) getLock() (lock.Lock, error) {
	switch cfg.buildLock {
	case "local":
		return lock.NewMemoryLock(), nil
	case "s3":
		lock, err := lock.NewS3Lock(lock.S3Config{
			Bucket:   cfg.s3Bucket,
			Endpoint: cfg.s3Endpoint,
			Region:   cfg.s3Region,
		})
		if err != nil {
			return nil, fmt.Errorf("creating s3 lock %w", err)
		}
		return lock, nil
	default:
		return nil, fmt.Errorf("invalid lock type: %s", cfg.buildLock)
	}
}
