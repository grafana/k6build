package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// newFakeS3Client returns a S3 client talking to a local test server driven by
// handler, so the SDK's own retry and timeout behavior can be exercised without
// hitting AWS.
func newFakeS3Client(t *testing.T, handler http.HandlerFunc) *s3.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("id", "secret", ""),
		BaseEndpoint: aws.String(server.URL),
		UsePathStyle: true,
	})
}

func headBucket(t *testing.T, client *s3.Client, opts ...func(*s3.Options)) error {
	t.Helper()

	_, err := client.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String("bucket")}, opts...)
	return err
}

func TestWithCallRetries_SucceedsFirstTry(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client := newFakeS3Client(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	err := headBucket(t, client, WithCallTimeout(time.Second), WithCallRetries(3, time.Millisecond))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

func TestWithCallRetries_RetriesOnServerError(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client := newFakeS3Client(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	err := headBucket(t, client, WithCallTimeout(time.Second), WithCallRetries(3, time.Millisecond))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 calls, got %d", got)
	}
}

func TestWithCallRetries_GivesUpAfterExhaustingRetries(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client := newFakeS3Client(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})

	err := headBucket(t, client, WithCallTimeout(time.Second), WithCallRetries(2, time.Millisecond))
	if err == nil {
		t.Fatal("expected error")
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 calls (1 + 2 retries), got %d", got)
	}
}

func TestWithCallRetries_NegativeRetriesStillCallsOnce(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client := newFakeS3Client(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})

	err := headBucket(t, client, WithCallTimeout(time.Second), WithCallRetries(-1, time.Millisecond))
	if err == nil {
		t.Fatal("expected error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

func TestWithCallTimeout_BoundsSingleAttempt(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client := newFakeS3Client(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			// simulate a stalled attempt that never returns until the client
			// gives up on it.
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	start := time.Now()
	err := headBucket(t, client, WithCallTimeout(20*time.Millisecond), WithCallRetries(3, time.Millisecond))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 calls, got %d", got)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected stalled attempts to fail fast on their own timeout, took %v", elapsed)
	}
}

func TestWithCallTimeout_FailsFastWithNoRetries(t *testing.T) {
	t.Parallel()

	client := newFakeS3Client(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	start := time.Now()
	err := headBucket(t, client, WithCallTimeout(20*time.Millisecond), WithCallRetries(0, time.Millisecond))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected call to fail fast instead of hanging, took %v", elapsed)
	}
}
