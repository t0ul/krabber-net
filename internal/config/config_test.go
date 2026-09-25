package config

import (
	"strings"
	"testing"
)

func TestParseProdRequiresSecrets(t *testing.T) {
	_, err := parse(map[string]string{"TABLE_NAME": "krabber-prod", "BASE_URL": "http://krabber.net"})
	if err == nil {
		t.Fatal("expected an error for prod without origin secret and with http BASE_URL")
	}
	for _, want := range []string{"ORIGIN_VERIFY_SECRET", "https"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

func TestParseProd(t *testing.T) {
	c, err := parse(map[string]string{
		"TABLE_NAME":                    "krabber-prod",
		"ORIGIN_VERIFY_SECRET":          "current",
		"ORIGIN_VERIFY_SECRET_PREVIOUS": "old",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL.String() != "https://krabber.net" || c.MailSender != "ses" || c.MailDailyCap != 500 || c.Addr != ":5000" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if len(c.OriginVerifySecrets) != 2 {
		t.Fatalf("secrets: %+v", c)
	}
}

func TestParseDevDefaults(t *testing.T) {
	c, err := parse(map[string]string{"APP_ENV": "dev", "TABLE_NAME": "krabber-dev", "BASE_URL": "http://localhost:5000"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsDev() || c.MailSender != "console" || c.TurnstileEnabled() || c.SignupMode != SignupOpen {
		t.Fatalf("dev defaults: %+v", c)
	}
}

func TestSignupMode(t *testing.T) {
	env := map[string]string{"APP_ENV": "dev", "TABLE_NAME": "krabber-dev", "BASE_URL": "http://localhost:5000", "SIGNUP_MODE": "Invite"}
	if c, err := parse(env); err != nil || c.SignupMode != SignupInvite {
		t.Fatalf("invite: %+v %v", c, err)
	}
	env["SIGNUP_MODE"] = "vip"
	if _, err := parse(env); err == nil || !strings.Contains(err.Error(), "SIGNUP_MODE") {
		t.Fatalf("bad mode: %v", err)
	}
}
