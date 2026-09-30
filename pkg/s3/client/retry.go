package client

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
)

const (
	// DefaultCallTimeout bounds a single attempt of a S3 API call. Without it, an
	// attempt can inherit whatever is left of the caller's context and, if the
	// connection stalls instead of erroring out (e.g. a network blip), silently
	// consume the caller's entire remaining deadline before the retryer gets a
	// chance to try again.
	DefaultCallTimeout = 10 * time.Second
	// DefaultCallRetries is the number of retries for a S3 API call that times out
	// or hits a network-level error, on top of the initial attempt.
	DefaultCallRetries = 2
	// DefaultCallBackoff is the wait between retries of a S3 API call.
	DefaultCallBackoff = 250 * time.Millisecond
)

// perAttemptTimeoutMiddlewareID identifies the finalize middleware added by
// WithCallTimeout. It is inserted right after the SDK's own retry middleware (whose
// ID is "Retry"), which re-enters everything after it on every attempt, so the
// timeout it sets applies per attempt rather than once for the whole (possibly
// retried) call.
const perAttemptTimeoutMiddlewareID = "PerAttemptTimeout"

// WithCallTimeout returns a S3 client call option that bounds each individual
// attempt of the call with timeout. A stalled attempt fails fast instead of
// blocking for the lifetime of the caller's context, letting the retryer try
// again; the sub-context it derives still can't outlive the caller's own context.
func WithCallTimeout(timeout time.Duration) func(*s3.Options) {
	return func(o *s3.Options) {
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			return stack.Finalize.Insert(
				middleware.FinalizeMiddlewareFunc(perAttemptTimeoutMiddlewareID,
					func(
						parentCtx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler,
					) (middleware.FinalizeOutput, middleware.Metadata, error) {
						attemptCtx, cancel := context.WithTimeout(parentCtx, timeout)
						defer cancel()
						out, meta, err := next.HandleFinalize(attemptCtx, in)
						return out, meta, tagPerAttemptTimeout(parentCtx, attemptCtx, err)
					},
				),
				"Retry",
				middleware.After,
			)
		})
	}
}

// WithCallRetries returns a S3 client call option that configures the SDK's
// built-in standard retryer to retry the call up to retries times, on top of the
// initial attempt, waiting a fixed backoff between attempts. A negative retries
// is treated as zero, so the call is always attempted at least once. Whether a
// failed attempt (timeout, network-level error, throttling, ...) is retried at
// all is decided by the SDK's own default error classification, with one
// addition: see perAttemptTimeoutRetryable.
func WithCallRetries(retries int, backoff time.Duration) func(*s3.Options) {
	if retries < 0 {
		retries = 0
	}

	return func(o *s3.Options) {
		o.Retryer = retry.NewStandard(func(so *retry.StandardOptions) {
			so.MaxAttempts = retries + 1
			so.Backoff = retry.BackoffDelayerFunc(
				func(int, error) (time.Duration, error) { return backoff, nil },
			)
			// Must run before the default retry.NoRetryCanceledError check.
			so.Retryables = append([]retry.IsErrorRetryable{perAttemptTimeoutRetryable{}}, so.Retryables...)
		})
	}
}

// perAttemptTimeoutError tags an error as having occurred because
// WithCallTimeout's own per-attempt deadline elapsed, as opposed to the
// caller's outer context. WithCallTimeout applies this tag itself, right
// where it derives the per-attempt context, since that is the only place
// able to tell the two apart: both produce a context reporting
// context.DeadlineExceeded (a child context propagates its parent's Err(),
// deadline or not), so matching on the error alone downstream cannot
// distinguish "this attempt's own timeout fired" from "the caller's overall
// deadline expired".
type perAttemptTimeoutError struct{ err error }

func (e *perAttemptTimeoutError) Error() string { return e.err.Error() }
func (e *perAttemptTimeoutError) Unwrap() error { return e.err }

// tagPerAttemptTimeout wraps err in a perAttemptTimeoutError if attemptCtx (a
// context.WithTimeout child of parentCtx) ended because attemptCtx's own
// deadline elapsed, as opposed to parentCtx ending. attemptCtx can only
// report DeadlineExceeded here if either its own timer fired or parentCtx
// did; parentCtx.Err() being nil at this point (checked immediately, before
// it has a chance to expire on its own) means it was ours, not the caller's.
func tagPerAttemptTimeout(parentCtx, attemptCtx context.Context, err error) error {
	if err != nil && parentCtx.Err() == nil && errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
		return &perAttemptTimeoutError{err: err}
	}
	return err
}

// perAttemptTimeoutRetryable reclassifies our own WithCallTimeout expiring as
// retryable. The SDK's HTTP client layer treats any request error observed
// after its context is done as a canceled request (smithy.CanceledError, which
// implements the CanceledError() bool marker), whether that context was
// actually canceled or its deadline simply elapsed; by default
// retry.NoRetryCanceledError then refuses to retry anything shaped like that.
// Since WithCallTimeout derives its own per-attempt context, an expiring
// attempt looks the same to the SDK as the caller canceling the whole
// operation, unless we tell it otherwise here.
//
// This only matches errors WithCallTimeout has tagged as its own per-attempt
// timeout expiring (see perAttemptTimeoutError), not the caller's outer
// context ending: a genuine caller-initiated cancellation or the caller's own
// deadline expiring still falls through (UnknownTernary) to the default
// "don't retry cancellations" behavior.
type perAttemptTimeoutRetryable struct{}

func (perAttemptTimeoutRetryable) IsErrorRetryable(err error) aws.Ternary {
	var timeoutErr *perAttemptTimeoutError
	if errors.As(err, &timeoutErr) {
		return aws.TrueTernary
	}
	return aws.UnknownTernary
}
