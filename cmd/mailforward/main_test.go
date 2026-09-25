package main

import (
	"strings"
	"testing"
)

func TestRewrite(t *testing.T) {
	raw := "Return-Path: <sandy@treedome.test>\r\n" +
		"DKIM-Signature: v=1; a=rsa-sha256;\r\n\tb=abc\r\n" +
		"From: Sandy Cheeks <sandy@treedome.test>\r\n" +
		"To: support@krabber.net\r\n" +
		"Subject: My account\r\n  is locked\r\n" +
		"Message-ID: <1@treedome.test>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Hi,\r\n\r\nI can't log in.\r\n"
	got, err := rewrite([]byte(raw), "Krabber support <support@krabber.net>", "[support] ")
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		"From: Krabber support <support@krabber.net>\r\n",
		"Reply-To: Sandy Cheeks <sandy@treedome.test>\r\n",
		"Subject: [support] My account\r\n  is locked\r\n",
		"To: support@krabber.net\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"\r\n\r\nHi,\r\n\r\nI can't log in.\r\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("forward lacks %q:\n%s", want, s)
		}
	}
	for _, gone := range []string{"Return-Path", "DKIM-Signature", "b=abc", "Message-ID", "From: Sandy"} {
		if strings.Contains(s, gone) {
			t.Errorf("forward still has %q", gone)
		}
	}

	// A Reply-To the sender set wins over their From.
	got, _ = rewrite([]byte("From: a@x.test\nReply-To: b@x.test\n\nhi\n"), "Krabber support <support@krabber.net>", "[support] [unverified] ")
	if !strings.Contains(string(got), "Reply-To: b@x.test") || !strings.Contains(string(got), "Subject: [support] [unverified] (no subject)") {
		t.Errorf("reply-to or subject: %s", got)
	}
}
