// Package linkcard fetches the title and description a web page gives for
// itself (OpenGraph tags, or plain HTML), for the text-only card under a molt.
// Only public HTTPS addresses are fetched: the address is checked after DNS
// resolution, at connect time, so a name that resolves to a private or
// metadata address is refused even if it changes between lookups.
package linkcard

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	timeout      = 3 * time.Second
	maxBody      = 256 << 10
	maxURL       = 2048
	maxRedirects = 3
	maxTitle     = 120
	maxDesc      = 200
	userAgent    = "KrabberBot/1.0 (+https://krabber.net)"
)

var (
	// ErrUnsafe is returned for addresses the fetcher won't visit.
	ErrUnsafe = errors.New("linkcard: address not allowed")
	// ErrNoCard is returned when the page has nothing to show.
	ErrNoCard = errors.New("linkcard: nothing to show")
)

// Card is what a page says about itself.
type Card struct {
	URL, Title, Description, Host string
}

// Normalize returns the form a card is cached under: https, lowercase host,
// no default port, user info or fragment. ok is false for anything that
// isn't a plain public-looking HTTPS address.
func Normalize(raw string) (string, bool) {
	if len(raw) > maxURL {
		return "", false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Opaque != "" {
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || (u.Port() != "" && u.Port() != "443") {
		return "", false
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u.Scheme, u.Host, u.Fragment, u.RawFragment = "https", host, "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), true
}

// Fetcher fetches cards. The zero value isn't usable; call New.
type Fetcher struct {
	client *http.Client
	dialTo string // tests only: connect here whatever the host
}

// New returns a Fetcher that only connects to public addresses.
func New() *Fetcher { return newFetcher(publicAddr, nil) }

func newFetcher(allow func(netip.Addr) bool, tlsConfig *tls.Config) *Fetcher {
	f := &Fetcher{}
	dialer := &net.Dialer{
		Timeout: timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !allow(ip.Unmap()) {
				return ErrUnsafe
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if f.dialTo != "" {
				addr = f.dialTo
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig:        tlsConfig,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 32 << 10,
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      true,
	}
	f.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("linkcard: too many redirects")
			}
			if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
				return ErrUnsafe
			}
			return nil
		},
	}
	return f
}

// Fetch reads the page at rawURL and returns its card.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Card, error) {
	u, ok := Normalize(rawURL)
	if !ok {
		return Card{}, ErrUnsafe
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Card{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9")
	res, err := f.client.Do(req)
	if err != nil {
		return Card{}, fmt.Errorf("linkcard: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return Card{}, fmt.Errorf("linkcard: status %d", res.StatusCode)
	}
	if mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type")); mt != "text/html" && mt != "application/xhtml+xml" {
		return Card{}, ErrNoCard
	}
	title, desc := parse(io.LimitReader(res.Body, maxBody))
	if title == "" {
		return Card{}, ErrNoCard
	}
	return Card{
		URL:         u,
		Title:       title,
		Description: desc,
		Host:        strings.TrimPrefix(strings.ToLower(res.Request.URL.Hostname()), "www."),
	}, nil
}

// parse reads the page head for the best title and description.
func parse(r io.Reader) (title, desc string) {
	var og, tw, plain, ogDesc, twDesc, metaDesc string
	z := html.NewTokenizer(r)
	inTitle := false
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return pick(og, tw, plain, ogDesc, twDesc, metaDesc)
		case html.TextToken:
			if inTitle && plain == "" {
				plain = string(z.Text())
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "title":
				inTitle = false
			case "head":
				return pick(og, tw, plain, ogDesc, twDesc, metaDesc)
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "title":
				inTitle = tt == html.StartTagToken
			case "body":
				return pick(og, tw, plain, ogDesc, twDesc, metaDesc)
			case "meta":
				if !hasAttr {
					continue
				}
				var key, content string
				for {
					k, v, more := z.TagAttr()
					switch string(k) {
					case "property", "name":
						key = strings.ToLower(string(v))
					case "content":
						content = string(v)
					}
					if !more {
						break
					}
				}
				switch key {
				case "og:title":
					og = content
				case "twitter:title":
					tw = content
				case "og:description":
					ogDesc = content
				case "twitter:description":
					twDesc = content
				case "description":
					metaDesc = content
				}
			}
		}
	}
}

func pick(og, tw, plain, ogDesc, twDesc, metaDesc string) (title, desc string) {
	return clean(first(og, tw, plain), maxTitle), clean(first(ogDesc, twDesc, metaDesc), maxDesc)
}

func first(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// clean makes page text safe to show on one line: valid UTF-8, no control
// characters, single spaces, at most limit characters.
func clean(s string, limit int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > limit {
		s = strings.TrimSpace(string([]rune(s)[:limit-1])) + "…"
	}
	return s
}

var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved and broadcast
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64, can reach IPv4 private space
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),      // discard
	netip.MustParsePrefix("2001::/32"),     // Teredo
	netip.MustParsePrefix("2001:db8::/32"), // documentation
	netip.MustParsePrefix("2002::/16"),     // 6to4, can embed private IPv4
}

// publicAddr reports whether ip is an ordinary public unicast address.
func publicAddr(ip netip.Addr) bool {
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, p := range reserved {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
