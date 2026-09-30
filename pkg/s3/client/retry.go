package client

import (
	"context"
	"errors"
	"net"
	"time"
)

const (
	// DefaultCallTimeout bounds a single S3 API call. Without it, a call can inherit
	// whatever is left of the caller's context and, if the connection stalls instead of
	// erroring out (e.g. a network blip), silently consume the caller's entire remaining
	// deadline before anyone gets a chance to retry.
	DefaultCallTimeout = 10 * time.Second
	// DefaultCallRetries number of retries for a S3 API call that times out or hits a
	// network-level error.
	DefaultCallRetries = 2
	// DefaultCallBackoff wait between retries of a S3 API call.
	DefaultCallBackoff = 250 * time.Millisecond
)

// WithRetry calls fn with a bounded sub-context derived from ctx, retrying up to
// retries times (with a fixed backoff) if the call times out or fails with a
// network-level error. Each attempt gets its own timeout, so a single stalled
// call fails fast instead of blocking for the lifetime of the caller's context;
// the sub-context still can't outlive ctx itself. fn is always called at least
// once, even if retries is negative.
func WithRetry(
	ctx context.Context,
	timeout time.Duration,
	retries int,
	backoff time.Duration,
	fn func(context.Context) error,
) error {
	if retries < 0 {
		retries = 0
	}

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		callCtx, cancel := context.WithTimeout(ctx, timeout)
		lastErr = fn(callCtx)
		cancel()

		if lastErr == nil || !isRetryable(lastErr) {
			return lastErr
		}
	}

	return lastErr
}

// isRetryable reports whether err indicates a transient failure worth retrying:
// our own per-call deadline expiring, or a network-level error (timeout,
// connection reset, DNS failure, etc.).
func isRetryable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	return errors.As(err, &netErr)
}
