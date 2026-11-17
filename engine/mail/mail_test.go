package mail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"
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

// Another front end brings its own words; the day's budget is shared.
func TestWithTemplates(t *testing.T) {
	sender := &fakeSender{}
	counter := &fakeCounter{}
	krabber := New(sender, counter, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	kusaza := krabber.WithTemplates(fstest.MapFS{"templates/activation.tmpl": {Data: []byte(
		`{{define "subject"}}Open your Kusaza account{{end}}` +
			`{{define "plainBody"}}Welcome, {{.UserName}}.{{end}}` +
			`{{define "htmlBody"}}<p>Welcome, {{.UserName}}.</p>{{end}}`)}})
	ctx := context.Background()
	if err := kusaza.Send(ctx, "w@example.com", "activation", map[string]string{"UserName": "wiley"}); err != nil {
		t.Fatal(err)
	}
	if got := sender.sent[0]; got.Subject != "Open your Kusaza account" || got.Text != "Welcome, wiley." {
		t.Fatalf("sent %+v", got)
	}
	if err := krabber.Send(ctx, "k@example.com", "activation", map[string]string{"UserName": "kingler", "Token": "t", "ActivateURL": "u", "ActivatePage": "p"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sender.sent[1].Subject, "Kusaza") {
		t.Fatalf("Krabber's own mailer took Kusaza's words: %q", sender.sent[1].Subject)
	}
	if err := kusaza.Send(ctx, "w@example.com", "activation", map[string]string{"UserName": "wiley"}); !errors.Is(err, ErrDailyCapReached) {
		t.Fatalf("a third email under a shared cap of 2: %v", err)
	}
}
