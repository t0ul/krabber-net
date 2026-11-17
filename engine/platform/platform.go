// Package platform sets up logging and AWS SDK configuration.
package platform

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/smithy-go/logging"
	"github.com/aws/smithy-go/middleware"
)

// NewLogger returns a JSON logger on stdout, which Beanstalk streams to CloudWatch.
func NewLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// AWSConfig loads the default credential chain (the instance role on Beanstalk,
// the developer's profile locally).
func AWSConfig(ctx context.Context, region string) (aws.Config, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return aws.Config{}, fmt.Errorf("load AWS config: %w", err)
	}
	return cfg, nil
}

// DynamoDB returns a client. A non-empty endpoint points it at DynamoDB Local,
// which accepts any static credentials.
func DynamoDB(cfg aws.Config, endpoint string) *dynamodb.Client {
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.APIOptions = append(o.APIOptions, func(s *middleware.Stack) error {
			return s.Initialize.Add(meterMiddleware, middleware.After)
		})
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.Credentials = credentials.NewStaticCredentialsProvider("local", "local", "")
			// DynamoDB Local's error responses trigger noisy SDK warnings.
			o.Logger = logging.Nop{}
		}
	})
}
