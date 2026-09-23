package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// turnstile verifies Cloudflare Turnstile responses. With no secret configured
// it is disabled and every check passes.
type turnstile struct {
	secret string
	url    string
	client *http.Client
}

func newTurnstile(secret string) *turnstile {
	return &turnstile{secret: secret, url: turnstileVerifyURL, client: &http.Client{Timeout: 5 * time.Second}}
}

func (t *turnstile) enabled() bool { return t.secret != "" }

// verify reports whether the widget response is valid for this visitor.
func (t *turnstile) verify(ctx context.Context, response, remoteIP string) (bool, error) {
	if !t.enabled() {
		return true, nil
	}
	if response == "" {
		return false, nil
	}
	form := url.Values{"secret": {t.secret}, "response": {response}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := t.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("turnstile verify: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return false, fmt.Errorf("turnstile verify: %w", err)
	}
	return body.Success, nil
}
