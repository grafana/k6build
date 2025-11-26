// Package test provides a s3 test environment using a localstack container
package test

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
)

// New creates a localstack container and returns a client
func New(ctx context.Context) (*s3.Client, func(context.Context), error) {
	localstackContainer, err := localstack.Run(ctx, "localstack/localstack:latest")
	if err != nil {
		return nil, nil, fmt.Errorf("localstack setup: %w", err)
	}

	terminate := func(ctx context.Context) {
		_ = localstackContainer.Terminate(ctx)
	}

	region := "us-east-1"
	host, err := localstackContainer.Host(ctx)
	if err != nil {
		return nil, nil, err
	}

	mappedPort, err := localstackContainer.MappedPort(ctx, nat.Port("4566/tcp"))
	if err != nil {
		return nil, nil, err
	}

	awsEndpoint := fmt.Sprintf("http://%s:%s", host, mappedPort.Port()) //nolint:nosprintfhostport
	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("accesskey", "secretkey", "token")),
	)
	if err != nil {
		return nil, nil, err
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(awsEndpoint)
		o.UsePathStyle = true
	})

	return client, terminate, nil
}
