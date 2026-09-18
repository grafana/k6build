package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/grafana/k6build/pkg/store"
	s3mock "github.com/grafana/s3-mock"
)

type object struct {
	id      string
	content []byte
}

// testObjectID is the object id reused across test cases that preload a single existing object.
const testObjectID = "existing-object"

func setupStore(t *testing.T, preload []object) store.ObjectStore {
	t.Helper()

	client, terminate, err := s3mock.New()
	if err != nil {
		t.Fatalf("setting up test %v", err)
	}
	t.Cleanup(
		func() { terminate(t.Context()) }, //nolint:errcheck
	)

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
			id:      testObjectID,
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
			id:        testObjectID,
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

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, obj.URL, nil)
			if err != nil {
				t.Fatalf("creating request %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
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
			id:      testObjectID,
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
			id:        testObjectID,
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

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, obj.URL, nil)
			if err != nil {
				t.Fatalf("creating request %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
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

func TestNewRejectsNegativeCallTimeout(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Bucket: "some-bucket", CallTimeout: -1 * time.Second})
	if !errors.Is(err, store.ErrInitializingStore) {
		t.Fatalf("expected %v, got %v", store.ErrInitializingStore, err)
	}
}

func TestNewRejectsNegativeCallRetries(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Bucket: "some-bucket", CallRetries: aws.Int(-1)})
	if !errors.Is(err, store.ErrInitializingStore) {
		t.Fatalf("expected %v, got %v", store.ErrInitializingStore, err)
	}
}

func TestNewRejectsNegativeCallBackoff(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Bucket: "some-bucket", CallBackoff: -1 * time.Second})
	if !errors.Is(err, store.ErrInitializingStore) {
		t.Fatalf("expected %v, got %v", store.ErrInitializingStore, err)
	}
}

// getObjectAttributesSuccessBody is a minimal, valid GetObjectAttributes response
// body: enough for the AWS SDK to populate ETag and Checksum.ChecksumSHA256,
// which is all Store.Get reads from the response.
const getObjectAttributesSuccessBody = `<?xml version="1.0" encoding="UTF-8"?>
<GetObjectAttributesResponse xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <ETag>&quot;abc123&quot;</ETag>
  <Checksum><ChecksumSHA256>c2FtcGxlLWNoZWNrc3Vt</ChecksumSHA256></Checksum>
</GetObjectAttributesResponse>`

// newRawS3TestClient points a real *s3.Client at srvURL, with the SDK's own
// built-in retryer disabled so that any retry observed by a test is
// attributable solely to s3client.WithRetry, not the SDK's default behavior.
func newRawS3TestClient(t *testing.T, srvURL string) *s3.Client {
	t.Helper()

	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")),
	)
	if err != nil {
		t.Fatalf("loading aws config %v", err)
	}

	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(srvURL)
		o.UsePathStyle = true
		o.Retryer = aws.NopRetryer{}
	})
}

func TestGet_BoundsEachAttemptToCallTimeout(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		// stall far longer than CallTimeout below, on every attempt
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	s, err := New(Config{
		Client:      newRawS3TestClient(t, srv.URL),
		Bucket:      "test-bucket",
		CallTimeout: 20 * time.Millisecond,
		CallRetries: aws.Int(1),
		CallBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("creating store %v", err)
	}

	start := time.Now()
	_, err = s.Get(t.Context(), "some-key")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected an error, got nil")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 attempts (1 + 1 retry), got %d", got)
	}
	// Each attempt is capped at 20ms, plus one 5ms backoff: ~45ms total.
	// The server actually stalls for 300ms per request, so this only holds
	// if the per-attempt timeout is genuinely being enforced.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("expected Get to give up well under the server's 300ms stall, took %v", elapsed)
	}
}

func TestGet_RetriesTransientFailureThenSucceeds(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			// simulate a network-level failure on the first attempt
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("test server response writer does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijacking connection %v", err)
			}
			conn.Close() //nolint:errcheck
			return
		}

		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(getObjectAttributesSuccessBody))
	}))
	t.Cleanup(srv.Close)

	s, err := New(Config{
		Client:      newRawS3TestClient(t, srv.URL),
		Bucket:      "test-bucket",
		CallRetries: aws.Int(1),
		CallBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("creating store %v", err)
	}

	obj, err := s.Get(t.Context(), "some-key")
	if err != nil {
		t.Fatalf("expected the retry to succeed, got %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 attempts (1 failure + 1 retry), got %d", got)
	}
	if obj.ID != "some-key" {
		t.Fatalf("expected object id %q, got %q", "some-key", obj.ID)
	}
}

func TestGet_StopsAfterConfiguredRetries(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server response writer does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijacking connection %v", err)
		}
		conn.Close() //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	s, err := New(Config{
		Client:      newRawS3TestClient(t, srv.URL),
		Bucket:      "test-bucket",
		CallRetries: aws.Int(2),
		CallBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("creating store %v", err)
	}

	_, err = s.Get(t.Context(), "some-key")
	if err == nil {
		t.Fatalf("expected an error, got nil")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 attempts (1 + 2 retries), got %d", got)
	}
}

func TestGet_ExplicitZeroRetriesDisablesRetrying(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server response writer does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijacking connection %v", err)
		}
		conn.Close() //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	// CallRetries is explicitly a pointer to 0, not omitted: this must disable
	// retries entirely rather than falling back to s3client.DefaultCallRetries.
	s, err := New(Config{
		Client:      newRawS3TestClient(t, srv.URL),
		Bucket:      "test-bucket",
		CallRetries: aws.Int(0),
		CallBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("creating store %v", err)
	}

	_, err = s.Get(t.Context(), "some-key")
	if err == nil {
		t.Fatalf("expected an error, got nil")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 attempt (retries explicitly disabled), got %d", got)
	}
}
