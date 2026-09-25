// Command deploy ships a Beanstalk bundle (make bundle) to krabber-prod:
// upload it, register it as an application version, switch the environment
// to it and wait until it's healthy, then invalidate /static/* on
// CloudFront. Deploys are immutable, so a new version that doesn't come up
// healthy is rolled back by Beanstalk and this command fails.
//
//	AWS_PROFILE=krabber-admin make deploy          # from a laptop
//	go run ./cmd/deploy -bucket B -distribution D  # what make deploy and CI run
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/t0ul/krabber-net/internal/platform"
)

func main() {
	bundle := flag.String("bundle", "dist/krabber.zip", "the bundle to deploy")
	label := flag.String("label", "", "version label (default: the commit, or the time)")
	app := flag.String("app", "krabber", "Beanstalk application")
	env := flag.String("env", "krabber-prod", "Beanstalk environment")
	bucket := flag.String("bucket", "", "artifacts bucket (terraform output artifacts_bucket)")
	distribution := flag.String("distribution", "", "CloudFront distribution to invalidate /static/* on (optional)")
	timeout := flag.Duration("timeout", 30*time.Minute, "how long to wait for the environment")
	flag.Parse()
	if *bucket == "" {
		log.Fatal("-bucket is required")
	}
	if *label == "" {
		*label = defaultLabel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cfg, err := platform.AWSConfig(ctx, "us-east-2")
	if err != nil {
		log.Fatal(err)
	}
	d := deployer{
		s3: s3.NewFromConfig(cfg), eb: elasticbeanstalk.NewFromConfig(cfg), cf: cloudfront.NewFromConfig(cfg),
		app: *app, env: *env, label: *label,
	}
	if err := d.run(ctx, *bundle, *bucket, *distribution); err != nil {
		log.Fatal(err)
	}
}

func defaultLabel() string {
	stamp := time.Now().UTC().Format("20060102-150405")
	if sha := os.Getenv("GITHUB_SHA"); len(sha) >= 7 {
		return sha[:7] + "-" + stamp
	}
	return "local-" + stamp
}

type deployer struct {
	s3              *s3.Client
	eb              *elasticbeanstalk.Client
	cf              *cloudfront.Client
	app, env, label string
}

func (d deployer) run(ctx context.Context, bundle, bucket, distribution string) error {
	f, err := os.Open(bundle) //nolint:gosec // the operator's own file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	key := "app/" + d.label + ".zip"
	log.Printf("uploading %s to s3://%s/%s", bundle, bucket, key)
	if _, err := d.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: &bucket, Key: &key, Body: f}); err != nil {
		return fmt.Errorf("upload: %w", err)
	}

	log.Printf("registering version %s", d.label)
	if _, err := d.eb.CreateApplicationVersion(ctx, &elasticbeanstalk.CreateApplicationVersionInput{
		ApplicationName: &d.app,
		VersionLabel:    &d.label,
		SourceBundle:    &ebtypes.S3Location{S3Bucket: &bucket, S3Key: &key},
		Process:         aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("create version: %w", err)
	}
	if err := d.waitProcessed(ctx); err != nil {
		return err
	}

	start := time.Now()
	log.Printf("deploying %s to %s (immutable: a fresh instance, then the switch)", d.label, d.env)
	if _, err := d.eb.UpdateEnvironment(ctx, &elasticbeanstalk.UpdateEnvironmentInput{
		EnvironmentName: &d.env,
		VersionLabel:    &d.label,
	}); err != nil {
		return fmt.Errorf("update environment: %w", err)
	}
	if err := d.waitHealthy(ctx, start); err != nil {
		return err
	}

	if distribution != "" {
		log.Printf("invalidating /static/* on %s", distribution)
		if _, err := d.cf.CreateInvalidation(ctx, &cloudfront.CreateInvalidationInput{
			DistributionId: &distribution,
			InvalidationBatch: &cftypes.InvalidationBatch{
				CallerReference: aws.String(d.label),
				Paths:           &cftypes.Paths{Quantity: aws.Int32(1), Items: []string{"/static/*"}},
			},
		}); err != nil {
			return fmt.Errorf("invalidate: %w", err)
		}
	}
	log.Printf("%s is live", d.label)
	return nil
}

func (d deployer) waitProcessed(ctx context.Context) error {
	for {
		out, err := d.eb.DescribeApplicationVersions(ctx, &elasticbeanstalk.DescribeApplicationVersionsInput{
			ApplicationName: &d.app,
			VersionLabels:   []string{d.label},
		})
		if err != nil {
			return fmt.Errorf("describe version: %w", err)
		}
		// The API answers PROCESSED where the SDK's constant says Processed.
		if len(out.ApplicationVersions) == 1 {
			status := string(out.ApplicationVersions[0].Status)
			switch {
			case strings.EqualFold(status, string(ebtypes.ApplicationVersionStatusProcessed)):
				return nil
			case strings.EqualFold(status, string(ebtypes.ApplicationVersionStatusFailed)):
				return errors.New("beanstalk couldn't process the bundle")
			}
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

// waitHealthy waits for the environment to settle, printing its events, and
// fails if it settled on another version (Beanstalk rolled back) or isn't
// healthy.
func (d deployer) waitHealthy(ctx context.Context, since time.Time) error {
	seen := since
	for {
		events, err := d.eb.DescribeEvents(ctx, &elasticbeanstalk.DescribeEventsInput{
			EnvironmentName: &d.env,
			StartTime:       aws.Time(seen),
		})
		if err == nil {
			for i := len(events.Events) - 1; i >= 0; i-- {
				e := events.Events[i]
				if e.EventDate != nil && e.EventDate.After(seen) {
					log.Printf("  %s %s", e.Severity, aws.ToString(e.Message))
					seen = *e.EventDate
				}
			}
		}
		out, err := d.eb.DescribeEnvironments(ctx, &elasticbeanstalk.DescribeEnvironmentsInput{
			EnvironmentNames: []string{d.env},
		})
		if err != nil {
			return fmt.Errorf("describe environment: %w", err)
		}
		if len(out.Environments) != 1 {
			return fmt.Errorf("environment %s not found", d.env)
		}
		e := out.Environments[0]
		if strings.EqualFold(string(e.Status), string(ebtypes.EnvironmentStatusReady)) {
			switch {
			case aws.ToString(e.VersionLabel) != d.label:
				return fmt.Errorf("beanstalk rolled back to %s; see the events above", aws.ToString(e.VersionLabel))
			case !strings.EqualFold(string(e.Health), string(ebtypes.EnvironmentHealthGreen)):
				return fmt.Errorf("deployed, but the environment is %s", e.Health)
			}
			return nil
		}
		if err := sleep(ctx, 15*time.Second); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("gave up waiting: %w", ctx.Err())
	case <-time.After(d):
		return nil
	}
}
