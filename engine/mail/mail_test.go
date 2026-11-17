package mail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type fakeCounter struct{ n int }

func (f *fakeCounter) Hit(context.Context, string, string, time.Duration) (int, error) {
	f.n++
	return f.n, nil
}

type fakeSender struct{ sent []Message }

func (f *fakeSender) Send(_ context.Context, m Message) error {
	f.sent = append(f.sent, m)
	return nil
}

func TestActivationEmailAndDailyCap(t *testing.T) {
	sender := &fakeSender{}
	m := New(sender, &fakeCounter{}, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))

	data := map[string]string{
		"UserName":     "<spongebob>",
		"Token":        "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"ActivateURL":  "https://krabber.net/krab/activate?token=ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"ActivatePage": "https://krabber.net/krab/activate",
	}
	if err := m.Send(context.Background(), "a@krabber.test", "activation", data); err != nil {
		t.Fatal(err)
	}
	got := sender.sent[0]
	if got.Subject != "Activate your Krabber account" {
		t.Errorf("subject = %q", got.Subject)
	}
	if !strings.Contains(got.Text, data["ActivateURL"]) || !strings.Contains(got.HTML, "&lt;spongebob&gt;") {
		t.Errorf("body missing link or HTML escaping:\n%s\n%s", got.Text, got.HTML)
	}

	err := m.Send(context.Background(), "b@krabber.test", "activation", data)
	if !errors.Is(err, ErrDailyCapReached) || len(sender.sent) != 1 {
		t.Fatalf("over cap: err=%v sent=%d", err, len(sender.sent))
	}
}
