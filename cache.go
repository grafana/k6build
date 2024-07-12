package k6build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Object represents an object stored in the Cache
// TODO: add metadata (e.g creation data, size)
type Object struct {
	ID       string
	Checksum string
	// an url for downloading the object's content
	URL string
}

func (o Object) String() string {
	buffer := &bytes.Buffer{}
	buffer.WriteString(fmt.Sprintf("id: %s", o.ID))
	buffer.WriteString(fmt.Sprintf(" checksum: %s", o.Checksum))
	buffer.WriteString(fmt.Sprintf("url: %s", o.URL))

	return buffer.String()
}

// Cache defines an interface for storing blobs
type Cache interface {
	// Get retrieves an objects if exists in the cache or an error otherwise
	Get(ctx context.Context, id string) (Object, error)
	// Store stores the object and returns the metadata
	Store(ctx context.Context, id string, content io.Reader) (Object, error)
	// Download returns the content of the object
	Download(ctx context.Context, object Object) (io.ReadCloser, error)
}

// FileCache a Cache backed by a file system
type FileCache struct {
	dir string
}

// NewTempFileCache creates a file cache using a temporary file
func NewTempFileCache() (Cache, error) {
	return NewFileCache(filepath.Join(os.TempDir(), "buildcache"))
}

// NewFileCache creates an cached backed by a directory
func NewFileCache(dir string) (Cache, error) {
	err := os.MkdirAll(dir, 0o750)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInitializingCache, err)
	}

	return &FileCache{
		dir: dir,
	}, nil
}

// Store stores the object and returns the metadata
func (f *FileCache) Store(_ context.Context, id string, content io.Reader) (Object, error) {
	if id == "" {
		return Object{}, fmt.Errorf("%w id cannot be empty", ErrCreatingObject)
	}

	if strings.Contains(id, string(os.PathSeparator)) {
		return Object{}, fmt.Errorf("%w id cannot contain %q", ErrCreatingObject, string(os.PathSeparator))
	}

	objectDir := filepath.Join(f.dir, id)
	// TODO: check permissions
	err := os.MkdirAll(objectDir, 0o750)
	if err != nil {
		return Object{}, fmt.Errorf("%w: %w", ErrCreatingObject, err)
	}

	objectFile, err := os.Create(filepath.Join(objectDir, "data")) //nolint:gosec
	if err != nil {
		return Object{}, fmt.Errorf("%w: %w", ErrCreatingObject, err)
	}

	// write content to object file and copy to buffer to calculate checksum
	// TODO: optimize memory by copying content in blocks
	buff := bytes.Buffer{}
	_, err = io.Copy(objectFile, io.TeeReader(content, &buff))
	if err != nil {
		return Object{}, fmt.Errorf("%w: %w", ErrCreatingObject, err)
	}

	// calculate checksum
	checksum := fmt.Sprintf("%x", sha256.Sum256(buff.Bytes()))

	// write metadata
	err = os.WriteFile(filepath.Join(objectDir, "checksum"), []byte(checksum), 0o644) //nolint:gosec
	if err != nil {
		return Object{}, fmt.Errorf("%w: %w", ErrCreatingObject, err)
	}

	return Object{
		ID:       id,
		Checksum: checksum,
		URL:      fmt.Sprintf("file://%s", objectFile.Name()),
	}, nil
}

// Get retrieves an objects if exists in the cache or an error otherwise
func (f *FileCache) Get(_ context.Context, id string) (Object, error) {
	objectDir := filepath.Join(f.dir, id)
	_, err := os.Stat(objectDir)

	if errors.Is(err, os.ErrNotExist) {
		return Object{}, fmt.Errorf("%w: %s", ErrObjectNotFound, id)
	}

	if err != nil {
		return Object{}, fmt.Errorf("%w: %w", ErrAccessingObject, err)
	}

	checksum, err := os.ReadFile(filepath.Join(objectDir, "checksum")) //nolint:gosec
	if err != nil {
		return Object{}, fmt.Errorf("%w: %w", ErrAccessingObject, err)
	}

	return Object{
		ID:       id,
		Checksum: string(checksum),
		URL:      fmt.Sprintf("file://%s", filepath.Join(objectDir, "data")),
	}, nil
}

// Download returns the content of the object given its url
func (f *FileCache) Download(_ context.Context, object Object) (io.ReadCloser, error) {
	url, err := url.Parse(object.URL)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAccessingObject, err)
	}

	switch url.Scheme {
	case "file":
		// prevent malicious path
		objectPath, err := f.sanitizePath(url.Path)
		if err != nil {
			return nil, err
		}

		objectFile, err := os.Open(objectPath) //nolint:gosec // path is sanitized
		if err != nil {
			// FIXE: is the path has invalid characters, still will return ErrNotExists
			if errors.Is(err, os.ErrNotExist) {
				return nil, ErrObjectNotFound
			}
			return nil, fmt.Errorf("%w: %w", ErrAccessingObject, err)
		}

		return objectFile, nil
	default:
		return nil, fmt.Errorf("%w unsupported schema: %s", ErrInvalidURL, url.Scheme)
	}
}

func (f *FileCache) sanitizePath(path string) (string, error) {
	path = filepath.Clean(path)

	if !filepath.IsAbs(path) || !strings.HasPrefix(path, f.dir) {
		return "", fmt.Errorf("%w : invalid path %s", ErrInvalidURL, path)
	}

	return path, nil
}
