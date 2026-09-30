package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeNetError is a minimal net.Error implementation for testing retryability.
type fakeNetError struct{}

func (fakeNetError) Error() string   { return "fake network error" }
func (fakeNetError) Timeout() bool   { return true }
func (fakeNetError) Temporary() bool { return true }

func TestWithRetry_NegativeRetriesStillCallsOnce(t *testing.T) {
	t.Parallel()

	calls := 0
	err := WithRetry(t.Context(), time.Second, -1, time.Millisecond, func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestWithRetry_SucceedsFirstTry(t *testing.T) {
	t.Parallel()

	calls := 0
	err := WithRetry(t.Context(), time.Second, 3, time.Millisecond, func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestWithRetry_RetriesOnNetworkError(t *testing.T) {
	t.Parallel()

	calls := 0
	err := WithRetry(t.Context(), time.Second, 3, time.Millisecond, func(context.Context) error {
		calls++
		if calls < 3 {
			return fakeNetError{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestWithRetry_RetriesOnDeadlineExceeded(t *testing.T) {
	t.Parallel()

	calls := 0
	err := WithRetry(t.Context(), 10*time.Millisecond, 3, time.Millisecond, func(callCtx context.Context) error {
		calls++
		if calls < 3 {
			// simulate a stalled call that never returns until its bounded
			// sub-context expires
			<-callCtx.Done()
			return callCtx.Err()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestWithRetry_GivesUpAfterExhaustingRetries(t *testing.T) {
	t.Parallel()

	calls := 0
	err := WithRetry(t.Context(), time.Second, 2, time.Millisecond, func(context.Context) error {
		calls++
		return fakeNetError{}
	})

	if !errors.Is(err, fakeNetError{}) {
		t.Fatalf("expected fakeNetError, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls (1 + 2 retries), got %d", calls)
	}
}

func TestWithRetry_DoesNotRetryNonTransientError(t *testing.T) {
	t.Parallel()

	nonTransient := errors.New("permanent failure")
	calls := 0
	err := WithRetry(t.Context(), time.Second, 3, time.Millisecond, func(context.Context) error {
		calls++
		return nonTransient
	})

	if !errors.Is(err, nonTransient) {
		t.Fatalf("expected nonTransient error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestWithRetry_StopsWhenOuterContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	calls := 0
	err := WithRetry(ctx, time.Second, 3, time.Hour, func(context.Context) error {
		calls++
		return fakeNetError{}
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// one attempt happens before the backoff wait observes the canceled context
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}
