# k6build

k6build builds custom k6 binaries with extensions.


## API

`k6build` defines an [API](build.go) for building custom k6 binaries.

The API returns the metadata of the custom binary, including an URL for downloading it,
but does not return the binary itself.

The request for building a binary specifies the target platform (required) and the dependencies,
including k6.

The dependencies specify the import path (as used in the k6 script) and the semantic version 
constrains.

Dependencies are mapped by a catalog to the corresponding go module that implements it. The catalog
also defines the available versions.

If a dependency doesn't specify a constrains, the latest version (according to the catalog) is used.

See [k6catalog](http://github.com/grafana/k6catalog) for more details on defining a catalog.

The default catalog is defined at https://registry.k6.io/catalog.json


## Usage scenarios

The following sections describe different usage scenarios.

### kubernetes

[examples/kubernetes](examples/kubernetes/) describes how to run `k6build` in a kubernetes cluster and execute `k6` tests in a pod using [k6exec](https://github.com/grafana/k6exec).

### k6-operator

TODO: use [k6-operator](https://github.com/grafana/k6-operator) for running the tests using a custom image.

<!-- #region cli -->
# k6build

**Build custom k6 binaries with extensions**

## Commands

* [k6build cache](#k6build-cache)	 - k6build cache server
* [k6build local](#k6build-local)	 - build custom k6 binary locally
* [k6build remote](#k6build-remote)	 - build a custom k6 using a remote build server
* [k6build server](#k6build-server)	 - k6 build service

---
# k6build cache

k6build cache server

## Synopsis


Starts a k6build cache server. 

The cache server offers a REST API for storing and downloading objects.

Objects can be retrieved by a download url returned when the object is stored.

The --download-url specifies the base URL for downloading objects. This is necessary to allow
downloading the objects from different machines.


```
k6build cache [flags]
```

## Examples

```

# start the cache server serving an external url
k6build cache --download-url http://external.url

# store object from same host
curl -x POST http://localhost:9000/cache/objectID -d "object content" | jq .
{
	"Error": "",
	"Object": {
	  "ID": "objectID",
	  "Checksum": "17d3eb873fe4b1aac4f9d2505aefbb5b53b9a7f34a6aadd561be104c0e9d678b",
	  "URL": "http://external.url:9000/cache/objectID/download"
	}
      }

# download object from another machine using the external url
curl http://external.url:9000/cache/objectID/download

```

## Flags

```
  -c, --cache-dir string      cache directory (default "/tmp/cache/objectstore")
  -d, --download-url string   base url used for downloading objects.
                              If not specified http://localhost:<port>/cache is used
  -h, --help                  help for cache
  -l, --log-level string      log level (default "INFO")
  -p, --port int              port server will listen (default 9000)
```

## SEE ALSO

* [k6build](#k6build)	 - Build custom k6 binaries with extensions

---
# k6build local

build custom k6 binary locally

## Synopsis


k6build local builder creates a custom k6 binary artifacts that satisfies certain
dependencies. Requires the golang toolchain and git.


```
k6build local [flags]
```

## Examples

```

# build k6 v0.51.0 with latest version of k6/x/kubernetes
k6build local -k v0.51.0 -d k6/x/kubernetes

platform: linux/amd64
k6: v0.51.0
k6/x/kubernetes: v0.9.0
checksum: 7f06720503c80153816b4ef9f58571c2fce620e0447fba1bb092188ff87e322d

# build k6 v0.51.0 with k6/x/kubernetes v0.8.0 and k6/x/output-kafka v0.7.0
k6build local -k v0.51.0 \
    -d k6/x/kubernetes:v0.8.0 \
    -d k6/x/output-kafka:v0.7.0

platform: linux/amd64
k6: v0.51.0
k6/x/kubernetes: v0.8.0
k6/x/output-kafka": v0.7.0
checksum: f4af178bb2e29862c0fc7d481076c9ba4468572903480fe9d6c999fea75f3793


# build k6 v0.50.0 with latest version of k6/x/kubernetes using a custom catalog
k6build local -k v0.50.0 -d k6/x/kubernetes \
    -c /path/to/catalog.json -q

# build k6 v0.50.0 using a custom GOPROXY
k6build local -k v0.50.0 -e GOPROXY=http://localhost:80 -q

```

## Flags

```
  -f, --cache-dir string         cache dir (default "/tmp/buildservice")
  -c, --catalog string           dependencies catalog (default "catalog.json")
  -g, --copy-go-env              copy go environment (default true)
  -d, --dependency stringArray   list of dependencies in form package:constrains
  -e, --env stringToString       build environment variables (default [])
  -h, --help                     help for local
  -k, --k6 string                k6 version constrains (default "*")
  -o, --output string            path to put the binary as an executable. (default "k6")
  -p, --platform string          target platform (default GOOS/GOARCH)
  -q, --quiet                    don't print artifact's details
  -v, --verbose                  print build process output
```

## SEE ALSO

* [k6build](#k6build)	 - Build custom k6 binaries with extensions

---
# k6build remote

build a custom k6 using a remote build server

## Synopsis


Builds custom k6 binaries using a k6build server returning the details of the
binary artifact and optionally download it.


```
k6build remote [flags]
```

## Examples

```

# build k6 v0.51.0 with k6/x/kubernetes v0.8.0 and k6/x/output-kafka v0.7.0
k6build remote -s http://localhost:8000 \
    -k v0.51.0 \
    -p linux/amd64 \
    -d k6/x/kubernetes:v0.8.0 \
    -d k6/x/output-kafka:v0.7.0

id: 62d08b13fdef171435e2c6874eaad0bb35f2f9c7
platform: linux/amd64
k6: v0.51.0
k6/x/kubernetes: v0.9.0
k6/x/output-kafka": v0.7.0
checksum: f4af178bb2e29862c0fc7d481076c9ba4468572903480fe9d6c999fea75f3793
url: http://localhost:8000/cache/62d08b13fdef171435e2c6874eaad0bb35f2f9c7/download


# build k6 v0.51 with k6/x/output-kafka v0.7.0 and download as 'build/k6'
k6build remote -s http://localhost:8000 \
    -p linux/amd64  \
    -k v0.51.0 -d k6/x/output-kafka:v0.7.0 \
    -o build/k6 -q

# check downloaded binary
build/k6 version
k6 v0.51.0 (go1.22.2, linux/amd64)
Extensions:
  github.com/grafana/xk6-output-kafka v0.7.0, xk6-kafka [output]

```

## Flags

```
  -d, --dependency stringArray   list of dependencies in form package:constrains
  -h, --help                     help for remote
  -k, --k6 string                k6 version constrains (default "*")
  -o, --output string            path to download the custom binary as an executable.
                                 If not specified, the artifact is not downloaded.
  -p, --platform string          target platform (default GOOS/GOARCH)
  -q, --quiet                    don't print artifact's details
  -s, --server string            url for build server (default "http://localhost:8000")
```

## SEE ALSO

* [k6build](#k6build)	 - Build custom k6 binaries with extensions

---
# k6build server

k6 build service

## Synopsis


Starts a k6build server

The server exposes an API for building custom k6 binaries.

The API returns the metadata of the custom binary, including an URL for downloading it,
but does not return the binary itself.

For example

	curl http://localhost:8000/build/ -d \
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
	  "url": "http://localhost:9000/cache/5a241ba6ff643075caadbd06d5a326e5e74f6f10/download",
	  "dependencies": {
	    "k6": "v0.50.0",
	    "k6/x/kubernetes": "v0.10.0"
	  },
	  "platform": "linux/amd64",
	  "checksum": "bfdf51ec9279e6d7f91df0a342d0c90ab4990ff1fb0215938505a6894edaf913"
	  }
	}

Note: The build server does not support CGO_ENABLE when building binaries
      due to this issue: https://github.com/grafana/k6build/issues/37
      use --enable-cgo=true to enable CGO support


```
k6build server [flags]
```

## Examples

```

# start the build server using a custom local catalog
k6build server -c /path/to/catalog.json

# start the build server using a custom GOPROXY
k6build server -e GOPROXY=http://localhost:80

```

## Flags

```
      --allow-prereleases    allow building pre-releases.
      --cache-url string     cache server url (default "http://localhost:9000/cache")
  -c, --catalog string       dependencies catalog. Can be path to a local file or an URL.
                              (default "https://registry.k6.io/catalog.json")
  -g, --copy-go-env          copy go environment (default true)
      --enable-cgo           enable CGO for building binaries.
  -e, --env stringToString   build environment variables (default [])
  -h, --help                 help for server
  -l, --log-level string     log level (default "INFO")
  -p, --port int             port server will listen (default 8000)
  -v, --verbose              print build process output
```

## SEE ALSO

* [k6build](#k6build)	 - Build custom k6 binaries with extensions

<!-- #endregion cli -->
