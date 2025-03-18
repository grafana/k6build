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
	// DefaultLeaseDuration is the default duration for a lock
	DefaultLeaseDuration = 5 * time.Minute
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
}

// S3Lock is a lock backed by a S3 bucket
type S3Lock struct {
	client *s3.Client
	bucket string
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

	return &S3Lock{
		client: client,
		bucket: conf.Bucket,
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
		sort.Slice(locks, func(i, j int) bool {
			return locks[i].LastModified.Before(*locks[j].LastModified)
		})

		if len(locks) == 1 {
			return release, nil
		}

		// look for the first lock that is not expired. We assume the last one is the most recent
		// and look for the first lock with a last modified time older than the most recent one minus
		// the lease duration
		first := 0
		last := len(locks) - 1
		for _, l := range locks {
			// if the lock is not expired break
			if l.LastModified.After(locks[last].LastModified.Add(-DefaultLeaseDuration)) {
				break
			}
			first++
		}

		if *locks[first].Key == lockID {
			return release, nil
		}

		time.Sleep(time.Second)
	}
}
