package lock

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	s3test "github.com/grafana/k6build/pkg/s3/test"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"golang.org/x/sync/errgroup"
)

func setupS3Lock(t *testing.T, lease, backoff time.Duration) Lock {
	t.Helper()

	client, terminate, err := s3test.New(t.Context())
	if err != nil {
		t.Fatalf("setup s3 client: %v", err)
	}
	t.Cleanup(
		func() { terminate(t.Context()) },
	)

	bucket := strings.ReplaceAll(strings.ToLower(t.Name()), "_", "-")
	_, err = client.CreateBucket(t.Context(), &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("s3 setup %v", err)
	}

	lock, err := NewS3Lock(S3Config{
		Client:  client,
		Bucket:  bucket,
		Lease:   lease,
		Backoff: backoff,
	})
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

	client, terminate, err := s3test.New(t.Context())
	if err != nil {
		t.Fatalf("setup s3 client: %v", err)
	}
	t.Cleanup(
		func() { terminate(t.Context()) },
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

	lock := setupS3Lock(t, defaultLease, defaultBackoff)

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

	lock := setupS3Lock(t, defaultLease, defaultBackoff)

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

	lock := setupS3Lock(t, 5*time.Second, 100*time.Millisecond)

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

	shortLease := 2 * time.Second
	shortBackoff := 100 * time.Millisecond
	lock := setupS3Lock(t, shortLease, shortBackoff)

	// Acquire a lock but don't release it
	_, err := lock.Lock(t.Context(), "test-resource")
	if err != nil {
		t.Fatalf("failed to acquire first lock: %v", err)
	}

	// Wait for the lease to expire
	time.Sleep(shortLease + time.Second)

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
