// Package s3 implements a s3-backed object store
package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/grafana/k6build"
	s3client "github.com/grafana/k6build/pkg/s3/client"
	"github.com/grafana/k6build/pkg/store"
)

// DefaultURLExpiration Default expiration for the presigned download URLs.
// After this time attempts to download the object will fail
// TODO: check this default (AWS default is 900 seconds)
const DefaultURLExpiration = time.Hour * 24

// Store a ObjectStore backed by a S3 bucket
type Store struct {
	bucket      string
	client      *s3.Client
	expiration  time.Duration
	callTimeout time.Duration
	callRetries int
	callBackoff time.Duration
}

// Config S3 Store configuration
type Config struct {
	Client *s3.Client
	// AWS endpoint (used for testing)
	Endpoint string
	// AWS Region
	Region string
	// Name of the S3 bucket
	Bucket string
	// Expiration for the presigned download URLs
	URLExpiration time.Duration
	// CallTimeout bounds the cache-check call (GetObjectAttributes) issued by Get, so
	// a stalled connection can't silently consume the caller's entire context budget.
	// Does not apply to Put. Defaults to s3client.DefaultCallTimeout. Must not be negative.
	CallTimeout time.Duration
	// CallRetries number of retries for Get's cache-check call if it times out or hits
	// a network-level error. Does not apply to Put. nil defaults to
	// s3client.DefaultCallRetries; a pointer to 0 explicitly disables retries.
	// Must not point to a negative value.
	CallRetries *int
	// CallBackoff wait between retries of Get's cache-check call. Does not apply to
	// Put. Defaults to s3client.DefaultCallBackoff. Must not be negative.
	CallBackoff time.Duration
}

// WithExpiration sets the expiration for the presigned URL
func WithExpiration(exp time.Duration) func(*s3.PresignOptions) {
	return func(opts *s3.PresignOptions) {
		opts.Expires = exp
	}
}

// New creates an object store backed by a S3 bucket
func New(conf Config) (store.ObjectStore, error) {
	var err error

	if conf.Bucket == "" {
		return nil, fmt.Errorf("%w: bucket name cannot be empty", store.ErrInitializingStore)
	}

	client := conf.Client
	if client == nil {
		client, err = s3client.New(s3client.Config{
			Region:   conf.Region,
			Endpoint: conf.Endpoint,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: error creating S3 client", store.ErrInitializingStore)
		}
	}

	expiration := conf.URLExpiration
	if expiration == 0 {
		expiration = DefaultURLExpiration
	}

	if conf.CallTimeout < 0 {
		return nil, fmt.Errorf("%w: call timeout cannot be negative", store.ErrInitializingStore)
	}
	if conf.CallRetries != nil && *conf.CallRetries < 0 {
		return nil, fmt.Errorf("%w: call retries cannot be negative", store.ErrInitializingStore)
	}
	if conf.CallBackoff < 0 {
		return nil, fmt.Errorf("%w: call backoff cannot be negative", store.ErrInitializingStore)
	}

	callTimeout := conf.CallTimeout
	if callTimeout == 0 {
		callTimeout = s3client.DefaultCallTimeout
	}

	callRetries := s3client.DefaultCallRetries
	if conf.CallRetries != nil {
		callRetries = *conf.CallRetries
	}

	callBackoff := conf.CallBackoff
	if callBackoff == 0 {
		callBackoff = s3client.DefaultCallBackoff
	}

	return &Store{
		client:      client,
		bucket:      conf.Bucket,
		expiration:  expiration,
		callTimeout: callTimeout,
		callRetries: callRetries,
		callBackoff: callBackoff,
	}, nil
}

// Put stores the object and returns the metadata
// Fails if the object already exists
func (s *Store) Put(ctx context.Context, id string, content io.Reader) (store.Object, error) {
	if id == "" {
		return store.Object{}, fmt.Errorf("%w: id cannot be empty", store.ErrCreatingObject)
	}

	buff, err := io.ReadAll(content)
	if err != nil {
		return store.Object{}, k6build.NewWrappedError(store.ErrCreatingObject, err)
	}

	checksum := sha256.Sum256(buff)
	_, err = s.client.PutObject(
		ctx,
		&s3.PutObjectInput{
			Bucket:            aws.String(s.bucket),
			Key:               aws.String(id),
			Body:              bytes.NewReader(buff),
			ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
			ChecksumSHA256:    aws.String(base64.StdEncoding.EncodeToString(checksum[:])),
			IfNoneMatch:       aws.String("*"),
		},
	)
	if err != nil {
		// check for duplicated object
		var aerr smithy.APIError
		if errors.As(err, &aerr) && aerr.ErrorCode() == "PreconditionFailed" {
			return store.Object{}, fmt.Errorf("%w: %q", store.ErrDuplicateObject, id)
		}
		return store.Object{}, k6build.NewWrappedError(store.ErrCreatingObject, err)
	}

	url, err := s.getDownloadURL(ctx, id)
	if err != nil {
		return store.Object{}, k6build.NewWrappedError(store.ErrCreatingObject, err)
	}

	return store.Object{
		ID:       id,
		Checksum: fmt.Sprintf("%x", checksum),
		URL:      url,
	}, nil
}

// Get retrieves an objects if exists in the object store or an error otherwise
func (s *Store) Get(ctx context.Context, id string) (store.Object, error) {
	var obj *s3.GetObjectAttributesOutput
	err := s3client.WithRetry(ctx, s.callTimeout, s.callRetries, s.callBackoff, func(callCtx context.Context) error {
		var getErr error
		obj, getErr = s.client.GetObjectAttributes(
			callCtx,
			&s3.GetObjectAttributesInput{
				Bucket: aws.String(s.bucket),
				Key:    aws.String(id),
				ObjectAttributes: []types.ObjectAttributes{
					types.ObjectAttributesChecksum,
					types.ObjectAttributesEtag,
				},
			},
		)
		return getErr
	})
	if err != nil {
		// check for object not found
		var aerr smithy.APIError
		if errors.As(err, &aerr) && aerr.ErrorCode() == "NoSuchKey" {
			return store.Object{}, fmt.Errorf("%w (%s)", store.ErrObjectNotFound, id)
		}

		return store.Object{}, k6build.NewWrappedError(store.ErrAccessingObject, err)
	}

	url, err := s.getDownloadURL(ctx, id)
	if err != nil {
		return store.Object{}, k6build.NewWrappedError(store.ErrAccessingObject, err)
	}

	checksum, err := base64.StdEncoding.DecodeString(*obj.Checksum.ChecksumSHA256)
	if err != nil {
		return store.Object{}, k6build.NewWrappedError(store.ErrAccessingObject, err)
	}
	return store.Object{
		ID:       id,
		Checksum: fmt.Sprintf("%x", checksum),
		URL:      url,
	}, nil
}

func (s *Store) getDownloadURL(ctx context.Context, id string) (string, error) {
	// create a presigned get request to get the download URL
	request, err := s3.NewPresignClient(s.client).PresignGetObject(
		ctx, &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(id),
		},
		WithExpiration(s.expiration),
	)
	if err != nil {
		return "", k6build.NewWrappedError(store.ErrCreatingObject, err)
	}

	return request.URL, nil
}
