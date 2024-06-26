package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/grafana/k6build"
	"github.com/grafana/k6catalog"
	"github.com/grafana/k6foundry"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	serverLong = `
starts a k6build server that server
`

	serverExample = `
# start the build server using a custom catalog
k6build server -c /path/to/catalog.json

# start the server the build server using a custom GOPROXY
k6build server -e GOPROXY=http://localhost:80`
)

// NewServer creates new cobra command for resolve command.
func NewServer() *cobra.Command { //nolint:funlen
	var (
		buildEnv  map[string]string
		cacheDir  string
		catalog   string
		copyGoEnv bool
		port      int
		verbose   bool
		log       *logrus.Logger
		logLevel  string
	)

	cmd := &cobra.Command{
		Use:     "server",
		Short:   "k6 build service",
		Long:    serverLong,
		Example: serverExample,
		// prevent the usage help to printed to stderr when an error is reported by a subcommand
		SilenceUsage: true,
		// this is needed to prevent cobra to print errors reported by subcommands in the stderr
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ll, err := logrus.ParseLevel(logLevel)
			if err != nil {
				return fmt.Errorf("parsing log level %w", err)
			}
			log = &logrus.Logger{
				Out:       os.Stderr,
				Formatter: new(logrus.TextFormatter),
				Level:     ll,
			}

			catalog, err := k6catalog.NewCatalogFromJSON(catalog)
			if err != nil {
				return fmt.Errorf("creating catalog %w", err)
			}

			builderOpts := k6foundry.NativeBuilderOpts{
				Verbose: verbose,
				GoOpts: k6foundry.GoOpts{
					Env:       buildEnv,
					CopyGoEnv: copyGoEnv,
				},
			}
			builder, err := k6foundry.NewNativeBuilder(cmd.Context(), builderOpts)
			if err != nil {
				return fmt.Errorf("creating builder %w", err)
			}

			cache, err := k6build.NewFileCache(cacheDir)
			if err != nil {
				return fmt.Errorf("creating cache %w", err)
			}

			// FIXME: this will not work across machines
			cacheSrvURL := fmt.Sprintf("http://localhost:%d/cache", port)
			config := k6build.CacheServerConfig{
				BaseURL: cacheSrvURL,
				Cache:   cache,
				Log:     log,
			}
			cacheSrv := k6build.NewCacheServer(config)

			cacheClientConfig := k6build.CacheClientConfig{
				Server: cacheSrvURL,
			}
			cacheClient, err := k6build.NewCacheClient(cacheClientConfig)
			if err != nil {
				return fmt.Errorf("creating cache client %w", err)
			}

			buildSrv := k6build.NewBuildService(
				catalog,
				builder,
				cacheClient,
			)

			apiConfig := k6build.APIServerConfig{
				BuildService: buildSrv,
				Log:          log,
			}
			buildAPI := k6build.NewAPIServer(apiConfig)

			srv := http.NewServeMux()
			srv.Handle("POST /build/", http.StripPrefix("/build", buildAPI))
			srv.Handle("/cache/", http.StripPrefix("/cache", cacheSrv))

			listerAddr := fmt.Sprintf("localhost:%d", port)
			log.Infof("starting server at %s", listerAddr)
			err = http.ListenAndServe(listerAddr, srv) //nolint:gosec
			if err != nil {
				log.Infof("server ended with error %s", err.Error())
			}
			log.Info("ending server")

			return nil
		},
	}

	cmd.Flags().StringVarP(&catalog, "catalog", "c", "catalog.json", "dependencies catalog")
	cmd.Flags().StringVarP(&cacheDir, "cache-dir", "f", "/tmp/buildservice", "cache dir")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "print build process output")
	cmd.Flags().BoolVarP(&copyGoEnv, "copy-go-env", "g", true, "copy go environment")
	cmd.Flags().StringToStringVarP(&buildEnv, "env", "e", nil, "build environment variables")
	cmd.Flags().IntVarP(&port, "port", "p", 8000, "port server will listen")
	cmd.Flags().StringVarP(&logLevel, "log-level", "l", "INFO", "log level")

	return cmd
}
