// Package mail renders and sends transactional email, enforcing a daily cap.
package mail

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	textTemplate "text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

//go:embed "templates"
var templateFS embed.FS

// ErrDailyCapReached means the day's email budget is used up.
var ErrDailyCapReached = errors.New("mail: daily cap reached")

// Message is a rendered email.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Counter is the rate-limit counter used for the daily cap.
type Counter interface {
	Hit(ctx context.Context, action, key string, window time.Duration) (int, error)
}

// Mailer renders templates and sends them through a Sender.
type Mailer struct {
	sender   Sender
	counter  Counter
	dailyCap int
	log      *slog.Logger
}

// New returns a Mailer. dailyCap 0 disables sending entirely.
func New(sender Sender, counter Counter, dailyCap int, log *slog.Logger) *Mailer {
	return &Mailer{sender: sender, counter: counter, dailyCap: dailyCap, log: log}
}

// Send renders the named template (for example "activation") with data and
// sends it, unless today's cap is reached.
func (m *Mailer) Send(ctx context.Context, to, name string, data any) error {
	msg, err := render(to, name, data)
	if err != nil {
		return err
	}
	n, err := m.counter.Hit(ctx, "mail", "daily", 24*time.Hour)
	if err != nil {
		return err
	}
	if n > m.dailyCap {
		m.log.Warn("mail_cap_reached", "cap", m.dailyCap, "template", name)
		return ErrDailyCapReached
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := m.sender.Send(ctx, msg); err != nil {
		return fmt.Errorf("send %s email: %w", name, err)
	}
	return nil
}

func render(to, name string, data any) (Message, error) {
	file := "templates/" + name + ".tmpl"
	textT, err := textTemplate.ParseFS(templateFS, file)
	if err != nil {
		return Message{}, fmt.Errorf("parse %s: %w", file, err)
	}
	htmlT, err := template.ParseFS(templateFS, file)
	if err != nil {
		return Message{}, fmt.Errorf("parse %s: %w", file, err)
	}
	var subject, text, html bytes.Buffer
	if err := textT.ExecuteTemplate(&subject, "subject", data); err != nil {
		return Message{}, err
	}
	if err := textT.ExecuteTemplate(&text, "plainBody", data); err != nil {
		return Message{}, err
	}
	if err := htmlT.ExecuteTemplate(&html, "htmlBody", data); err != nil {
		return Message{}, err
	}
	return Message{To: to, Subject: subject.String(), Text: text.String(), HTML: html.String()}, nil
}

// SESSender sends through the SES v2 API.
type SESSender struct {
	Client           *sesv2.Client
	From             string
	ConfigurationSet string
}

// Send implements Sender.
func (s *SESSender) Send(ctx context.Context, m Message) error {
	_, err := s.Client.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress:     aws.String(s.From),
		Destination:          &types.Destination{ToAddresses: []string{m.To}},
		ConfigurationSetName: aws.String(s.ConfigurationSet),
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(m.Subject), Charset: aws.String("UTF-8")},
			Body: &types.Body{
				Text: &types.Content{Data: aws.String(m.Text), Charset: aws.String("UTF-8")},
				Html: &types.Content{Data: aws.String(m.HTML), Charset: aws.String("UTF-8")},
			},
		}},
	})
	return err
}

// ConsoleSender logs emails instead of sending them (local development).
type ConsoleSender struct {
	Log *slog.Logger
}

// Send implements Sender.
func (c *ConsoleSender) Send(_ context.Context, m Message) error {
	c.Log.Info("email (not sent: dev console sender)", "to", m.To, "subject", m.Subject, "body", m.Text)
	return nil
}
