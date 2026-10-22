// Package botkit is the shared spine for the Krabber bots (@gm, @barnacle,
// @holidays, …). Each bot is a tiny cmd/<bot> that composes one molt's text;
// botkit handles reading the API key from SSM (or API_KEY locally), posting to
// the Krabber API with the bearer key, and the Lambda-vs-local run modes. This
// mirrors how cmd/scuttle works, without copying its plumbing into every bot.
//
// Local use (prints only, never posts):
//
//	DRY_RUN=1 go run ./cmd/gm
//
// Local use against a dev server (actually posts):
//
//	API_KEY=kb_... KRABBER_API_URL=http://localhost:5050/api/v1/molts go run ./cmd/gm
package botkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// MoltMax is the Krabber molt character limit.
const MoltMax = 280

const defaultUA = "krabber-bot (+https://krabber.net; support@krabber.net)"

// Client posts molts to the Krabber API with a bot's bearer key.
type Client struct {
	http   *http.Client
	apiURL string
	apiKey string
	ua     string
}

// NewClient builds a Client, reading the API URL, user agent, and key from the
// environment / SSM. It is only needed to post; a dry run never calls it.
func NewClient(ctx context.Context) (*Client, error) {
	key, err := apiKey(ctx)
	if err != nil {
		return nil, err
	}
	return &Client{
		http:   &http.Client{Timeout: 15 * time.Second},
		apiURL: Getenv("KRABBER_API_URL", "https://krabber.net/api/v1/molts"),
		ua:     Getenv("USER_AGENT", defaultUA),
		apiKey: key,
	}, nil
}

// Post publishes one molt.
func (c *Client) Post(ctx context.Context, text string) error {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.ua)

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if res.StatusCode != http.StatusCreated {
		return fmt.Errorf("api returned %d: %s", res.StatusCode, string(bytes.TrimSpace(body)))
	}
	return nil
}

// Run is a bot's main(): it calls compose to build the molt text, then on Lambda
// (EventBridge) or a one-shot local run posts it. With DRY_RUN set it prints the
// text and never touches the API (so no key is needed). An empty text from
// compose is a no-op (nothing to say right now), not an error.
func Run(name string, compose func(ctx context.Context) (string, error)) {
	do := func(ctx context.Context) error {
		text, err := compose(ctx)
		if err != nil {
			return err
		}
		if text == "" {
			log.Printf("[%s] nothing to post", name)
			return nil
		}
		if os.Getenv("DRY_RUN") != "" {
			log.Printf("[%s] dry-run (%d chars): %s", name, utf8.RuneCountInString(text), text)
			return nil
		}
		c, err := NewClient(ctx)
		if err != nil {
			return err
		}
		if err := c.Post(ctx, text); err != nil {
			return fmt.Errorf("post: %w", err)
		}
		log.Printf("[%s] posted: %s", name, text)
		return nil
	}

	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		lambda.Start(do)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := do(ctx); err != nil {
		log.Fatal(err)
	}
}

// Fit returns the first candidate within MoltMax (by rune count), else the last
// one hard-trimmed to MoltMax runes. Build candidates widest-first.
func Fit(candidates ...string) string {
	for _, s := range candidates {
		if utf8.RuneCountInString(s) <= MoltMax {
			return s
		}
	}
	last := candidates[len(candidates)-1]
	r := []rune(last)
	if len(r) > MoltMax {
		return string(r[:MoltMax])
	}
	return last
}

// Truncate shortens s to at most n runes, ending with an ellipsis if it cut.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}

// Getenv returns the env var or a fallback.
func Getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// apiKey reads the Krabber API key from API_KEY (local) or SSM (API_KEY_PARAM).
func apiKey(ctx context.Context) (string, error) {
	if k := os.Getenv("API_KEY"); k != "" {
		return k, nil
	}
	name := os.Getenv("API_KEY_PARAM")
	if name == "" {
		return "", fmt.Errorf("set API_KEY (local) or API_KEY_PARAM (SSM)")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("aws config: %w", err)
	}
	out, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("read %s from ssm: %w", name, err)
	}
	return aws.ToString(out.Parameter.Value), nil
}
