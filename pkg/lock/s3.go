package lock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/grafana/k6build"
	s3client "github.com/grafana/k6build/pkg/s3/client"
)

const (
	// DefaultLease is the default duration for a lock
	defaultLease = time.Minute

	// Default backoff between checks for lease
	defaultBackoff = time.Second

	// default maximum time a lock can be held
	defaultMaxLease = 5 * time.Minute
)

// S3Config S3 Lock configuration
type S3Config struct {
	Client *s3.Client
	// AWS endpoint (used for testing)
	Endpoint string
	// AWS Region
	Region string
	// Name of the S3 bucket
	Bucket string
	// Lease duration for locks
	Lease time.Duration
	// Backoff for lock checks
	Backoff time.Duration
	// Grace period to keep lease
	Grace time.Duration
	// Maximum lease time
	MaxLease time.Duration
}

// S3Lock is a lock backed by a S3 bucket
// Creates an object for the lock and checks until this lock is the oldest non-expired one
type S3Lock struct {
	client   *s3.Client
	bucket   string
	lease    time.Duration
	backoff  time.Duration
	grace    time.Duration
	maxLease time.Duration
}

// NewS3Lock creates a lock backed by a S3 bucket
// The lock is obtained when it is the older non-expired lock for the given id
// The lock is released by deleting the object
// The lock is considered expired if the object is older than the lease duration
func NewS3Lock(conf S3Config) (Lock, error) {
	var err error

	if conf.Bucket == "" {
		return nil, fmt.Errorf("%w: bucket name cannot be empty", ErrCofig)
	}

	client := conf.Client
	if client == nil {
		client, err = s3client.New(s3client.Config{
			Region:   conf.Region,
			Endpoint: conf.Endpoint,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: error creating S3 client", ErrCofig)
		}
	}

	backoff := conf.Backoff
	if backoff == 0 {
		backoff = defaultBackoff
	}

	lease := conf.Lease
	if lease == 0 {
		lease = defaultLease
	}

	grace := conf.Grace
	if grace == 0 {
		grace = lease * 3
	}

	maxLease := conf.MaxLease
	if maxLease == 0 {
		maxLease = defaultMaxLease
	}
	return &S3Lock{
		client:   client,
		bucket:   conf.Bucket,
		lease:    lease,
		backoff:  backoff,
		grace:    grace,
		maxLease: maxLease,
	}, nil
}

// Lock creates a lock for the given id. The lock is released when the returned function is called
func (s *S3Lock) Lock(ctx context.Context, id string) (func(context.Context) error, error) {
	for {
		acquired, release, err := s.Try(ctx, id)
		if err != nil {
			return nil, err
		}

		if acquired {
			return release, nil
		}

		time.Sleep(s.backoff)
	}
}

// Try attempts to reserve a lock for a given id. Returns a bool indicating if it could reserve it.
// If the lock was acquired, returns a function that releases the lock.
func (s *S3Lock) Try(ctx context.Context, id string) (bool, func(context.Context) error, error) {
	lockID := fmt.Sprintf("%s.lock", id)
	lockObject, err := s.client.PutObject(
		ctx,
		&s3.PutObjectInput{
			Bucket:      aws.String(s.bucket),
			Key:         aws.String(lockID),
			Body:        bytes.NewReader([]byte{}),
			IfNoneMatch: aws.String("*"),
		},
	)

	// we got the lock
	// TODO refactor into separate function
	if err == nil {
		// program a periodic update of the lease.
		updateCtx, cancelUpdate := context.WithCancel(ctx)
		go updater(
			updateCtx,
			s.client,
			s.bucket,
			lockID,
			lockObject.ETag,
			s.lease,
			s.maxLease,
		)

		// release function releases the global lock
		release := func(ctx context.Context) error {
			// stop updates
			cancelUpdate()

			_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket:  aws.String(s.bucket),
				Key:     aws.String(lockID),
				IfMatch: lockObject.ETag,
			})
			if err != nil {
				return k6build.NewWrappedError(ErrLocking, err)
			}
			return nil
		}
		return true, release, nil
	}

	// check for duplicated object
	var aerr smithy.APIError
	if errors.As(err, &aerr) && aerr.ErrorCode() == "PreconditionFailed" {
		lock, errGet := s.client.GetObjectAttributes(
			ctx,
			&s3.GetObjectAttributesInput{
				Bucket: aws.String(s.bucket),
				Key:    aws.String(lockID),
				ObjectAttributes: []types.ObjectAttributes{
					types.ObjectAttributesEtag,
				},
			})

		// if the lock still exists and it is expired, try to delete
		if errGet == nil && time.Since(lock.LastModified.Local()) > s.grace {
			_, _ = s.client.DeleteObject(
				ctx,
				&s3.DeleteObjectInput{
					Bucket:  aws.String(s.bucket),
					Key:     aws.String(lockID),
					IfMatch: lock.ETag,
				})
		}
		return false, nil, nil
	}

	return false, nil, k6build.NewWrappedError(ErrLocking, err)
}

// update until the context is done of the ticker is stopped by the release function
func updater(
	ctx context.Context,
	client *s3.Client,
	bucket string,
	lockID string,
	lockETag *string,
	lease time.Duration,
	maxLease time.Duration,
) {
	ticker := time.NewTicker(lease)
	start := time.Now()

	for {
		select {
		case <-ctx.Done():
			ticker.Stop()
			return
		case tick := <-ticker.C: // try update
			// prevent runaway locks. Stop after max lease
			if tick.Sub(start) > maxLease {
				ticker.Stop()
				return
			}
			_, err := client.PutObject(
				ctx,
				&s3.PutObjectInput{
					Bucket:  aws.String(bucket),
					Key:     aws.String(lockID),
					Body:    bytes.NewReader([]byte{}),
					IfMatch: lockETag,
				},
			)
			if err != nil {
				ticker.Stop()
				return
			}
		}
	}
}
