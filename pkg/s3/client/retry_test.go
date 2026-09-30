package client

import (
	"context"
	"errors"
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

func headBucketWithContext(t *testing.T, ctx context.Context, client *s3.Client, opts ...func(*s3.Options)) error {
	t.Helper()

	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String("bucket")}, opts...)
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

func TestWithCallTimeout_DoesNotRetryOnCallerContextExpiry(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client := newFakeS3Client(t, func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// simulate a stalled attempt that never returns, so the caller's own
		// deadline (not the per-attempt timeout) is what ends the call.
		<-r.Context().Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := headBucketWithContext(t, ctx, client, WithCallTimeout(time.Second), WithCallRetries(3, time.Millisecond))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 call, since the caller's own deadline expiring should not be retried, got %d", got)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected call to fail fast instead of hanging, took %v", elapsed)
	}
}

func TestTagPerAttemptTimeout_TagsOwnDeadline(t *testing.T) {
	t.Parallel()

	// parentCtx has plenty of time left; attemptCtx's own, much shorter
	// deadline is what ends it.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), time.Second)
	defer parentCancel()
	attemptCtx, attemptCancel := context.WithTimeout(parentCtx, time.Millisecond)
	defer attemptCancel()
	<-attemptCtx.Done()

	err := tagPerAttemptTimeout(parentCtx, attemptCtx, errors.New("boom"))

	var timeoutErr *perAttemptTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected a *perAttemptTimeoutError, got %v (%T)", err, err)
	}
}

func TestTagPerAttemptTimeout_DoesNotTagCallerDeadline(t *testing.T) {
	t.Parallel()

	// parentCtx's own deadline is shorter than attemptCtx's, so it is
	// parentCtx expiring that ends attemptCtx too; both then report
	// DeadlineExceeded, per Go's context propagation.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer parentCancel()
	attemptCtx, attemptCancel := context.WithTimeout(parentCtx, time.Second)
	defer attemptCancel()
	<-attemptCtx.Done()

	if !errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("expected attemptCtx to also report DeadlineExceeded, got %v", attemptCtx.Err())
	}

	err := tagPerAttemptTimeout(parentCtx, attemptCtx, errors.New("boom"))

	var timeoutErr *perAttemptTimeoutError
	if errors.As(err, &timeoutErr) {
		t.Fatalf("expected the caller's own deadline expiring not to be tagged as our per-attempt timeout, got %v", err)
	}
}

func TestTagPerAttemptTimeout_DoesNotTagCallerCancellation(t *testing.T) {
	t.Parallel()

	parentCtx, parentCancel := context.WithCancel(context.Background())
	attemptCtx, attemptCancel := context.WithTimeout(parentCtx, time.Second)
	defer attemptCancel()
	parentCancel()
	<-attemptCtx.Done()

	err := tagPerAttemptTimeout(parentCtx, attemptCtx, errors.New("boom"))

	var timeoutErr *perAttemptTimeoutError
	if errors.As(err, &timeoutErr) {
		t.Fatalf("expected caller cancellation not to be tagged as our per-attempt timeout, got %v", err)
	}
}

func TestPerAttemptTimeoutRetryable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want aws.Ternary
	}{
		{"tagged as our own per-attempt timeout", &perAttemptTimeoutError{err: errors.New("boom")}, aws.TrueTernary},
		{"untagged deadline exceeded", context.DeadlineExceeded, aws.UnknownTernary},
		{"unrelated error", errors.New("boom"), aws.UnknownTernary},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := (perAttemptTimeoutRetryable{}).IsErrorRetryable(tc.err); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
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
