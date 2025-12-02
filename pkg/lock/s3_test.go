package lock

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	s3mock "github.com/grafana/s3-mock"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"golang.org/x/sync/errgroup"
)

func setupS3Lock(t *testing.T, conf S3Config) Lock {
	t.Helper()

	client, terminate, err := s3mock.New()
	if err != nil {
		t.Fatalf("setup s3 client: %v", err)
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

	conf.Client = client
	conf.Bucket = bucket

	lock, err := NewS3Lock(conf)
	if err != nil {
		t.Fatalf("create lock: %v", err)
	}

	return lock
}

func TestNewS3Lock(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Skipping test: localstack test container is failing in darwin and windows")
	}

	client, terminate, err := s3mock.New()
	if err != nil {
		t.Fatalf("setup s3 client: %v", err)
	}
	t.Cleanup(
		func() { terminate(t.Context()) }, //nolint:errcheck
	)

	testCases := []struct {
		title     string
		config    S3Config
		expectErr error
	}{
		{
			title: "valid config with client",
			config: S3Config{
				Client: client,
				Bucket: "test-bucket",
			},
			expectErr: nil,
		},
		{
			title: "valid config with custom lease and backoff",
			config: S3Config{
				Client:  client,
				Bucket:  "test-bucket",
				Lease:   2 * time.Minute,
				Backoff: 500 * time.Millisecond,
			},
			expectErr: nil,
		},
		{
			title: "empty bucket name",
			config: S3Config{
				Client: client,
				Bucket: "",
			},
			expectErr: ErrCofig,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.title, func(t *testing.T) {
			t.Parallel()

			lock, err := NewS3Lock(tc.config)
			if !errors.Is(err, tc.expectErr) {
				t.Fatalf("expected error %v, got %v", tc.expectErr, err)
			}

			if tc.expectErr == nil && lock == nil {
				t.Fatal("expected non-nil lock")
			}
		})
	}
}

func TestS3Lock_BasicLocking(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Skipping test: localstack test container is failing in darwin and windows")
	}

	lock := setupS3Lock(t, S3Config{})

	// Test basic lock acquisition and release
	release, err := lock.Lock(t.Context(), "test-resource")
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	// Release the lock
	err = release(t.Context())
	if err != nil {
		t.Fatalf("failed to release lock: %v", err)
	}

	// Should be able to acquire lock again after release
	release2, err := lock.Lock(t.Context(), "test-resource")
	if err != nil {
		t.Fatalf("failed to acquire lock second time: %v", err)
	}

	err = release2(t.Context())
	if err != nil {
		t.Fatalf("failed to release lock second time: %v", err)
	}
}

func TestS3Lock_MultipleLocks(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Skipping test: localstack test container is failing in darwin and windows")
	}

	lock := setupS3Lock(t, S3Config{})

	// Test acquiring locks for different resources
	release1, err := lock.Lock(t.Context(), "resource-1")
	if err != nil {
		t.Fatalf("failed to acquire lock for resource-1: %v", err)
	}

	release2, err := lock.Lock(t.Context(), "resource-2")
	if err != nil {
		t.Fatalf("failed to acquire lock for resource-2: %v", err)
	}

	release3, err := lock.Lock(t.Context(), "resource-3")
	if err != nil {
		t.Fatalf("failed to acquire lock for resource-3: %v", err)
	}

	// Release all locks
	if err := release1(t.Context()); err != nil {
		t.Fatalf("failed to release lock 1: %v", err)
	}
	if err := release2(t.Context()); err != nil {
		t.Fatalf("failed to release lock 2: %v", err)
	}
	if err := release3(t.Context()); err != nil {
		t.Fatalf("failed to release lock 3: %v", err)
	}
}

func TestS3Lock_Concurrent(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Skipping test: localstack test container is failing in darwin and windows")
	}

	conf := S3Config{
		Lease:   500 * time.Millisecond,
		Backoff: 100 * time.Millisecond,
	}
	lock := setupS3Lock(t, conf)

	const numGoroutines = 3
	const resourceID = "shared-resource"

	wg := new(errgroup.Group)

	// Launch multiple goroutines trying to acquire the same lock
	for id := range numGoroutines {
		wg.Go(func() error {
			release, err := lock.Lock(t.Context(), resourceID)
			if err != nil {
				return fmt.Errorf("goroutine %d: failed to acquire lock: %w", id, err)
			}

			t.Logf("goroutine %d: acquired lock", id)

			// Simulate some work between 100 and 1000 ms
			time.Sleep(time.Duration(100+rand.Intn(900)) * time.Millisecond)

			// Release the lock
			if err := release(t.Context()); err != nil {
				return fmt.Errorf("goroutine %d: released lock", id)
			}
			t.Logf("goroutine %d: acquired lock", id)
			return nil
		})
	}

	err := wg.Wait()
	if err != nil {
		t.Fatalf("error %v", err)
	}
}

func TestS3Lock_ExpiredLock(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Skipping test: localstack test container is failing in darwin and windows")
	}

	conf := S3Config{
		Lease:    100 * time.Millisecond,
		Backoff:  100 * time.Millisecond,
		MaxLease: 500 * time.Millisecond,
	}
	lock := setupS3Lock(t, conf)

	// Acquire a lock but don't release it
	_, err := lock.Lock(t.Context(), "test-resource")
	if err != nil {
		t.Fatalf("failed to acquire first lock: %v", err)
	}

	// give time for the lock to reach the max lease.
	// The updates should stop and the lock be released
	time.Sleep(600 * time.Millisecond)

	// Try to acquire the lock again - should succeed because the first lock expired
	release2, err := lock.Lock(t.Context(), "test-resource")
	if err != nil {
		t.Fatalf("failed to acquire lock after expiration: %v", err)
	}

	err = release2(t.Context())
	if err != nil {
		t.Fatalf("failed to release second lock: %v", err)
	}
}

func TestS3Lock_CancelContext(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Skipping test: localstack test container is failing in darwin and windows")
	}

	conf := S3Config{
		Lease:   100 * time.Millisecond,
		Backoff: 100 * time.Millisecond,
		Grace:   200 * time.Millisecond,
	}
	lock := setupS3Lock(t, conf)

	// Acquire a lock but don't release it
	ctx, cancel := context.WithCancel(t.Context())
	_, err := lock.Lock(ctx, "test-resource")
	if err != nil {
		t.Fatalf("failed to acquire first lock: %v", err)
	}

	// cancel context, should stop lease update
	cancel()

	// give time for the lock to pass its lease update grace period
	time.Sleep(300 * time.Millisecond)

	// Try to acquire the lock again - should succeed because the first lock expired
	release2, err := lock.Lock(t.Context(), "test-resource")
	if err != nil {
		t.Fatalf("failed to acquire lock after expiration: %v", err)
	}

	err = release2(t.Context())
	if err != nil {
		t.Fatalf("failed to release second lock: %v", err)
	}
}
