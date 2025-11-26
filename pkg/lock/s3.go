package lock

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/google/uuid"

	"github.com/grafana/k6build"
	s3client "github.com/grafana/k6build/pkg/s3/client"
)

const (
	// DefaultLease is the default duration for a lock
	defaultLease = time.Minute

	// Default backoff between checks for lease
	defaultBackoff = time.Second
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
}

// S3Lock is a lock backed by a S3 bucket
// Creates an object for the lock and checks until this lock is the oldest non-expired one
type S3Lock struct {
	client  *s3.Client
	bucket  string
	lease   time.Duration
	backoff time.Duration
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

	return &S3Lock{
		client:  client,
		bucket:  conf.Bucket,
		lease:   lease,
		backoff: backoff,
	}, nil
}

// Lock creates a lock for the given id. The lock is released when the returned function is called
func (s *S3Lock) Lock(ctx context.Context, id string) (func(context.Context) error, error) {
	lockID := fmt.Sprintf("%s.lock.%s", id, uuid.New().String())
	_, err := s.client.PutObject(
		ctx,
		&s3.PutObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(lockID),
			Body:   bytes.NewReader([]byte{}),
		},
	)
	if err != nil {
		return nil, k6build.NewWrappedError(ErrLocking, err)
	}

	release := func(ctx context.Context) error {
		_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(lockID),
		})
		if err != nil {
			return k6build.NewWrappedError(ErrLocking, err)
		}
		return nil
	}

	for {
		// we are assuming here that this call returns all the locks for the object
		// this seems reasonable as these are locks for building the same object
		// and we are deleting them in most cases
		result, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.bucket),
			Prefix: aws.String(fmt.Sprintf("%s.lock.", id)),
		})
		if err != nil {
			return nil, k6build.NewWrappedError(ErrLocking, err)
		}

		locks := result.Contents
		if len(locks) == 1 {
			return release, nil
		}

		// sort oldest first. Break ties using id
		sort.Slice(locks, func(i, j int) bool {
			diff := locks[i].LastModified.Sub(*locks[j].LastModified)
			if diff < 0 {
				return true
			}

			if diff == 0 {  // same timestamp, break by id
				return *locks[i].Key < *locks[j].Key
			}

			return false
		})

		// search for the first lock that is not expired. We use the LastUpdate of the newest (last)
		// lock to calculate the expiration limit. Any lock older than this limit is considered expired.
		// We don't use the local time to ensure times are synchronized by s3 clock
		first := 0
		last := len(locks) - 1
		expirationLimit := locks[last].LastModified.Add(- s.lease)
		for _, l := range locks {
			// if the lock is not expired we found it
			if l.LastModified.After(expirationLimit) {
				break
			}
			first++
		}

		// if the first non expired is our lock, return it
		if *locks[first].Key == lockID {
			return release, nil
		}

		// sleep the backoff time
		time.Sleep(s.backoff)
	}
}
