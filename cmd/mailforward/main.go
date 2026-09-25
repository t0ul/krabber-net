// Command mailforward is the Lambda behind support@krabber.net. SES stores
// each message in S3 and invokes it; it re-sends the message to FORWARD_TO
// from the support address (SES only sends from verified identities), with
// Reply-To set to the original sender so a reply goes back to them. Mail
// that failed SES's spam or virus scan is dropped.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/mail"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

type forwarder struct {
	s3        *s3.Client
	ses       *sesv2.Client
	bucket    string
	prefix    string
	from      string // Krabber support <support@krabber.net>
	fromEmail string // support@krabber.net
	to        string
}

func main() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	from := os.Getenv("FROM")
	addr, err := mail.ParseAddress(from)
	if err != nil {
		log.Fatalf("FROM: %v", err)
	}
	f := forwarder{
		s3: s3.NewFromConfig(cfg), ses: sesv2.NewFromConfig(cfg),
		bucket: os.Getenv("BUCKET"), prefix: os.Getenv("PREFIX"),
		from: from, fromEmail: addr.Address, to: os.Getenv("FORWARD_TO"),
	}
	lambda.Start(f.handle)
}

func (f forwarder) handle(ctx context.Context, e events.SimpleEmailEvent) error {
	for _, r := range e.Records {
		id := r.SES.Mail.MessageID
		if r.SES.Receipt.SpamVerdict.Status == "FAIL" || r.SES.Receipt.VirusVerdict.Status == "FAIL" {
			log.Printf("dropped %s: spam %s, virus %s", id, r.SES.Receipt.SpamVerdict.Status, r.SES.Receipt.VirusVerdict.Status)
			continue
		}
		obj, err := f.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: &f.bucket, Key: aws.String(f.prefix + id)})
		if err != nil {
			return fmt.Errorf("read %s: %w", id, err)
		}
		raw, err := io.ReadAll(obj.Body)
		_ = obj.Body.Close()
		if err != nil {
			return fmt.Errorf("read %s: %w", id, err)
		}
		msg, err := rewrite(raw, f.from)
		if err != nil {
			log.Printf("dropped %s: %v", id, err)
			continue
		}
		if _, err := f.ses.SendEmail(ctx, &sesv2.SendEmailInput{
			FromEmailAddress: &f.fromEmail,
			Destination:      &types.Destination{ToAddresses: []string{f.to}},
			Content:          &types.EmailContent{Raw: &types.RawMessage{Data: msg}},
		}); err != nil {
			return fmt.Errorf("forward %s: %w", id, err)
		}
		log.Printf("forwarded %s", id)
	}
	return nil
}

// dropped are the headers replaced or made wrong by forwarding: the sender's
// identity and signatures (SES signs the forward as krabber.net).
var dropped = map[string]bool{
	"from": true, "sender": true, "reply-to": true, "return-path": true,
	"dkim-signature": true, "domainkey-signature": true, "arc-seal": true,
	"arc-message-signature": true, "arc-authentication-results": true,
	"message-id": true,
}

// rewrite re-addresses a raw message for forwarding: From becomes from,
// Reply-To the original sender (or their Reply-To), the subject gets
// "[support] ", and the body is left exactly as it was.
func rewrite(raw []byte, from string) ([]byte, error) {
	sep := []byte("\r\n\r\n")
	end := bytes.Index(raw, sep)
	if end < 0 {
		sep = []byte("\n\n")
		if end = bytes.Index(raw, sep); end < 0 {
			return nil, fmt.Errorf("no header/body separator")
		}
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	replyTo := parsed.Header.Get("Reply-To")
	if replyTo == "" {
		replyTo = parsed.Header.Get("From")
	}

	var out bytes.Buffer
	var field []string // a header and its continuation lines
	flush := func() {
		if len(field) == 0 {
			return
		}
		name, _, _ := strings.Cut(field[0], ":")
		name = strings.ToLower(strings.TrimSpace(name))
		switch {
		case dropped[name]:
		case name == "subject":
			value := strings.TrimSpace(strings.Join(field, "\r\n")[len("subject:"):])
			out.WriteString("Subject: [support] " + value + "\r\n")
		default:
			out.WriteString(strings.Join(field, "\r\n") + "\r\n")
		}
		field = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw[:end]), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			field = append(field, line)
			continue
		}
		flush()
		field = []string{line}
	}
	flush()
	if parsed.Header.Get("Subject") == "" {
		out.WriteString("Subject: [support] (no subject)\r\n")
	}
	out.WriteString("From: " + from + "\r\n")
	if replyTo != "" {
		out.WriteString("Reply-To: " + replyTo + "\r\n")
	}
	out.WriteString("\r\n")
	out.Write(raw[end+len(sep):])
	return out.Bytes(), nil
}
