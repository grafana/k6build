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
						ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler,
					) (middleware.FinalizeOutput, middleware.Metadata, error) {
						ctx, cancel := context.WithTimeout(ctx, timeout)
						defer cancel()
						return next.HandleFinalize(ctx, in)
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
// errors.Is(err, context.DeadlineExceeded) only matches when this specific
// attempt's own deadline elapsed, not when the caller's outer context was
// canceled: canceling a parent context makes a derived context.WithTimeout's
// Err() report context.Canceled, not context.DeadlineExceeded. So a genuine
// caller-initiated cancellation still falls through (UnknownTernary) to the
// default "don't retry cancellations" behavior.
type perAttemptTimeoutRetryable struct{}

func (perAttemptTimeoutRetryable) IsErrorRetryable(err error) aws.Ternary {
	if errors.Is(err, context.DeadlineExceeded) {
		return aws.TrueTernary
	}
	return aws.UnknownTernary
}
