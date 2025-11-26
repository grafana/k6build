package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"

	s3test "github.com/grafana/k6build/pkg/s3/test"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/grafana/k6build/pkg/store"
)

type object struct {
	id      string
	content []byte
}

func setupStore(t *testing.T, preload []object) store.ObjectStore {
	t.Helper()

	client, terminate, err := s3test.New(t.Context())
	if err != nil {
		t.Fatalf("setting up test %v", err)
	}
	t.Cleanup(func() {
		terminate(t.Context())
	})

	bucket := strings.ReplaceAll(strings.ToLower(t.Name()), "_", "-")
	_, err = client.CreateBucket(t.Context(), &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("s3 setup %v", err)
	}

	for _, o := range preload {
		checksum := sha256.Sum256(o.content)
		_, err = client.PutObject(
			t.Context(),
			&s3.PutObjectInput{
				Bucket:            aws.String(bucket),
				Key:               aws.String(o.id),
				Body:              bytes.NewReader(o.content),
				ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
				ChecksumSHA256:    aws.String(base64.StdEncoding.EncodeToString(checksum[:])),
			},
		)
		if err != nil {
			t.Fatalf("preload setup %v", err)
		}
	}

	store, err := New(Config{Client: client, Bucket: bucket})
	if err != nil {
		t.Fatalf("create store %v", err)
	}

	return store
}

func TestPutObject(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Skipping test: localstack test container is failing in darwin and windows")
	}

	preload := []object{
		{
			id:      "existing-object",
			content: []byte("content"),
		},
	}

	s := setupStore(t, preload)

	testCases := []struct {
		title     string
		preload   []object
		id        string
		content   []byte
		expectErr error
	}{
		{
			title:   "put object",
			id:      "new-object",
			content: []byte("content"),
		},
		{
			title:     "put existing object",
			id:        "existing-object",
			content:   []byte("new content"),
			expectErr: store.ErrDuplicateObject,
		},
		{
			title:   "put empty object",
			id:      "empty",
			content: nil,
		},
		{
			title:     "put empty id",
			id:        "",
			content:   []byte("content"),
			expectErr: store.ErrCreatingObject,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			obj, err := s.Put(context.TODO(), tc.id, bytes.NewBuffer(tc.content))
			if !errors.Is(err, tc.expectErr) {
				t.Fatalf("expected %v got %v", tc.expectErr, err)
			}

			// if expected error, don't validate object
			if tc.expectErr != nil {
				return
			}

			_, err = url.Parse(obj.URL)
			if err != nil {
				t.Fatalf("invalid url %v", err)
			}

			resp, err := http.Get(obj.URL)
			if err != nil {
				t.Fatalf("reading object url %v", err)
			}
			defer resp.Body.Close() //nolint:errcheck

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("reading object url %s", resp.Status)
			}

			content, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading object content %v", err)
			}

			if !bytes.Equal(tc.content, content) {
				t.Fatalf("expected %v got %v", tc.content, content)
			}
		})
	}
}

func TestGetObject(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Skipping test: localstack test container is failing in darwin and windows")
	}

	preload := []object{
		{
			id:      "existing-object",
			content: []byte("content"),
		},
	}

	s := setupStore(t, preload)

	testCases := []struct {
		title     string
		preload   []object
		id        string
		expect    []byte
		expectErr error
	}{
		{
			title:     "get existing object",
			id:        "existing-object",
			expect:    []byte("content"),
			expectErr: nil,
		},
		{
			title:     "get non-existing object",
			id:        "non-existing-object",
			expectErr: store.ErrObjectNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			obj, err := s.Get(context.TODO(), tc.id)
			if !errors.Is(err, tc.expectErr) {
				t.Fatalf("expected %v got %v", tc.expectErr, err)
			}

			// if expected error, don't validate object
			if tc.expectErr != nil {
				return
			}

			_, err = url.Parse(obj.URL)
			if err != nil {
				t.Fatalf("invalid url %v", err)
			}

			resp, err := http.Get(obj.URL)
			if err != nil {
				t.Fatalf("reading object url %v", err)
			}
			defer resp.Body.Close() //nolint:errcheck

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("reading object url %s", resp.Status)
			}

			content, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading object content %v", err)
			}

			if !bytes.Equal(tc.expect, content) {
				t.Fatalf("expected %v got %v", tc.expect, content)
			}
		})
	}
}
