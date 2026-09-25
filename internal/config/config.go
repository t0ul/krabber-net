// Package config loads settings from environment variables and, in production,
// from SSM Parameter Store.
package config

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// Config is everything the web server needs at startup.
type Config struct {
	Env     string // "dev" or "prod"
	Addr    string
	BaseURL *url.URL
	Region  string

	TableName      string
	DynamoEndpoint string // DynamoDB Local; dev only

	// OriginVerifySecrets are the accepted values of the X-Origin-Verify header
	// that CloudFront adds. Two are accepted so the secret can be rotated.
	OriginVerifySecrets []string

	MailFrom          string
	MailDailyCap      int
	MailSender        string // "ses" or "console"
	MailConfiguration string // SES configuration set (bounce and complaint events)

	// ContactEmail is shown on the terms and privacy pages and in ban
	// emails; those leave the address out when it's empty.
	ContactEmail string

	TurnstileSiteKey string
	TurnstileSecret  string

	// SignupMode is who can create an account: SignupOpen (anyone; an
	// invite code is optional), SignupInvite (only with a krab's invite
	// code) or SignupClosed.
	SignupMode string

	// MaxKrabs closes signups once this many krabs can sign in, to keep the
	// bill bounded; 0 means no cap. Accounts that never activate don't count.
	MaxKrabs int

	// LogAllRequests logs every request with its DynamoDB units instead of a
	// 5% sample (always on in dev).
	LogAllRequests bool
}

// Signup modes.
const (
	SignupOpen   = "open"
	SignupInvite = "invite"
	SignupClosed = "closed"
)

// IsDev reports whether the server runs locally over plain HTTP.
func (c *Config) IsDev() bool { return c.Env == "dev" }

// TurnstileEnabled reports whether bot checks are configured.
func (c *Config) TurnstileEnabled() bool { return c.TurnstileSiteKey != "" && c.TurnstileSecret != "" }

// SSMGetter is the subset of the SSM client used by Load.
type SSMGetter interface {
	GetParametersByPath(context.Context, *ssm.GetParametersByPathInput, ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error)
}

// Load reads environment variables and then, if SSM_PREFIX is set, overlays
// parameters stored under that path (for example /krabber/prod/mail_from).
func Load(ctx context.Context, newSSM func(region string) (SSMGetter, error)) (*Config, error) {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}

	if prefix := env["SSM_PREFIX"]; prefix != "" {
		client, err := newSSM(valueOr(env["AWS_REGION"], "us-east-2"))
		if err != nil {
			return nil, err
		}
		params, err := loadSSM(ctx, client, prefix)
		if err != nil {
			return nil, err
		}
		for k, v := range params {
			env[strings.ToUpper(k)] = v
		}
	}

	return parse(env)
}

func loadSSM(ctx context.Context, client SSMGetter, prefix string) (map[string]string, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	out := map[string]string{}
	p := ssm.NewGetParametersByPathPaginator(client, &ssm.GetParametersByPathInput{
		Path:           aws.String(prefix),
		Recursive:      aws.Bool(true),
		WithDecryption: aws.Bool(true),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("read SSM parameters under %s: %w", prefix, err)
		}
		for _, param := range page.Parameters {
			name := strings.TrimPrefix(aws.ToString(param.Name), prefix+"/")
			out[name] = aws.ToString(param.Value)
		}
	}
	return out, nil
}

func parse(env map[string]string) (*Config, error) {
	c := &Config{
		Env:               valueOr(env["APP_ENV"], "prod"),
		Addr:              ":" + valueOr(env["PORT"], "5000"),
		Region:            valueOr(env["AWS_REGION"], "us-east-2"),
		TableName:         env["TABLE_NAME"],
		DynamoEndpoint:    env["DYNAMO_ENDPOINT"],
		MailFrom:          valueOr(env["MAIL_FROM"], "Krabber <no-reply@krabber.net>"),
		MailSender:        env["MAIL_SENDER"],
		MailConfiguration: valueOr(env["MAIL_CONFIGURATION_SET"], "krabber-transactional"),
		ContactEmail:      strings.TrimSpace(env["CONTACT_EMAIL"]),
		TurnstileSiteKey:  env["TURNSTILE_SITE_KEY"],
		TurnstileSecret:   env["TURNSTILE_SECRET"],
		SignupMode:        strings.ToLower(valueOr(env["SIGNUP_MODE"], SignupOpen)),
		LogAllRequests:    env["LOG_ALL_REQUESTS"] == "true",
		OriginVerifySecrets: nonEmpty(
			env["ORIGIN_VERIFY_SECRET"],
			env["ORIGIN_VERIFY_SECRET_PREVIOUS"],
		),
	}

	var errs []error

	if c.Env != "dev" && c.Env != "prod" {
		errs = append(errs, fmt.Errorf("APP_ENV must be dev or prod, got %q", c.Env))
	}

	base, err := url.Parse(valueOr(env["BASE_URL"], "https://krabber.net"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		errs = append(errs, fmt.Errorf("BASE_URL is not an absolute URL: %q", env["BASE_URL"]))
	}
	c.BaseURL = base

	capStr := valueOr(env["MAIL_DAILY_CAP"], "500")
	c.MailDailyCap, err = strconv.Atoi(capStr)
	if err != nil || c.MailDailyCap < 0 {
		errs = append(errs, fmt.Errorf("MAIL_DAILY_CAP must be a non-negative integer, got %q", capStr))
	}

	maxStr := valueOr(env["MAX_KRABS"], "0")
	c.MaxKrabs, err = strconv.Atoi(maxStr)
	if err != nil || c.MaxKrabs < 0 {
		errs = append(errs, fmt.Errorf("MAX_KRABS must be a non-negative integer, got %q", maxStr))
	}

	if c.MailSender == "" {
		c.MailSender = "ses"
		if c.Env == "dev" {
			c.MailSender = "console"
		}
	}
	if c.MailSender != "ses" && c.MailSender != "console" {
		errs = append(errs, fmt.Errorf("MAIL_SENDER must be ses or console, got %q", c.MailSender))
	}

	if c.TableName == "" {
		errs = append(errs, errors.New("TABLE_NAME is required"))
	}

	if c.Env == "prod" {
		if base != nil && base.Scheme != "https" {
			errs = append(errs, errors.New("BASE_URL must be https in prod"))
		}
		if len(c.OriginVerifySecrets) == 0 {
			errs = append(errs, errors.New("ORIGIN_VERIFY_SECRET is required in prod"))
		}
		if c.DynamoEndpoint != "" {
			errs = append(errs, errors.New("DYNAMO_ENDPOINT must not be set in prod"))
		}
		if c.MailSender != "ses" {
			errs = append(errs, errors.New("MAIL_SENDER must be ses in prod"))
		}
	}

	switch c.SignupMode {
	case SignupOpen, SignupInvite, SignupClosed:
	default:
		errs = append(errs, fmt.Errorf("SIGNUP_MODE must be open, invite or closed, got %q", c.SignupMode))
	}

	if (c.TurnstileSiteKey == "") != (c.TurnstileSecret == "") {
		errs = append(errs, errors.New("TURNSTILE_SITE_KEY and TURNSTILE_SECRET must be set together"))
	}

	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
