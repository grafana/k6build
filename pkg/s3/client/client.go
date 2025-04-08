// Package client implements utilities to instantiate an s3 client
package client

import (
	"context"
	"errors"

	"github.com/grafana/k6build"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var ErrConfig = errors.New("error configuring S3 client") //nolint:revive

// Config S3 Store configuration
type Config struct {
	// AWS endpoint (used for testing)
	Endpoint string
	// AWS Region
	Region string
}

// returns the S3 client options
func (c Config) s3Opts() []func(o *s3.Options) {
	opts := []func(o *s3.Options){}

	if c.Endpoint != "" {
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(c.Endpoint)
			o.UsePathStyle = true
		})
	}
	return opts
}

// returns the aws configuration load options from Config
func (c Config) awsOpts() []func(*config.LoadOptions) error {
	opts := []func(*config.LoadOptions) error{}

	if c.Region != "" {
		opts = append(opts, config.WithRegion(c.Region))
	}

	return opts
}

// New creates an object store backed by a S3 bucket
func New(conf Config) (*s3.Client, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO(), conf.awsOpts()...)
	if err != nil {
		return nil, k6build.NewWrappedError(ErrConfig, err)
	}
	client := s3.NewFromConfig(cfg, conf.s3Opts()...)

	return client, nil
}
