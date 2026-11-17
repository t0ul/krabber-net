package linkcard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	good := map[string]string{
		"https://Krabber.NET":             "https://krabber.net/",
		"https://krabber.net:443/a?b=1#c": "https://krabber.net/a?b=1",
		"HTTPS://krabber.net./x":          "https://krabber.net/x",
	}
	for in, want := range good {
		if got, ok := Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"http://krabber.net", "https://krabber.net:8443/", "https://user:pw@krabber.net/",
		"javascript:alert(1)", "https:///nohost", "ftp://krabber.net", "https://" + strings.Repeat("a", 2100) + ".test",
	} {
		if got, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) accepted as %q", in, got)
		}
	}
}

func TestPublicAddr(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0",
		"255.255.255.255", "::1", "::", "fe80::1", "fd00:ec2::254", "64:ff9b::a00:1", "2002:a00:1::1", "2001:0:1::1",
	} {
		if publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s allowed", s)
		}
	}
	if publicAddr(netip.MustParseAddr("::ffff:10.0.0.1").Unmap()) {
		t.Error("IPv4-mapped private address allowed")
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s refused", s)
		}
	}
}

const page = `<!doctype html><html><head>
<title>Plain title</title>
<meta property="og:title" content="  The Krusty   Krab &amp; You ">
<meta name="description" content="Home of the Krabby Patty.">
<meta name="twitter:description" content="Best burgers in Bikini Bottom.">
</head><body><meta property="og:title" content="ignored after the head"></body></html>`

func testServer(t *testing.T) (*httptest.Server, *Fetcher) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
	mux.HandleFunc("/plain", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><head><title>\n  Only\ta title \n</title></head></html>"))
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"no"}`))
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.test/", http.StatusFound)
	})
	mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/plain", http.StatusFound)
	})
	mux.HandleFunc("/missing", http.NotFound)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	f := newFetcher(func(netip.Addr) bool { return true }, srv.Client().Transport.(*http.Transport).TLSClientConfig)
	f.dialTo = srv.Listener.Addr().String()
	return srv, f
}

func TestFetch(t *testing.T) {
	_, f := testServer(t)
	ctx := context.Background()

	c, err := f.Fetch(ctx, "https://www.example.com/#top")
	if err != nil {
		t.Fatal(err)
	}
	want := Card{URL: "https://www.example.com/", Title: "The Krusty Krab & You", Description: "Best burgers in Bikini Bottom.", Host: "example.com"}
	if c != want {
		t.Errorf("card = %+v, want %+v", c, want)
	}

	if c, err := f.Fetch(ctx, "https://example.com/same"); err != nil || c.Title != "Only a title" || c.Description != "" {
		t.Errorf("same-host redirect: %+v %v", c, err)
	}
	if _, err := f.Fetch(ctx, "https://example.com/away"); !errors.Is(err, ErrUnsafe) {
		t.Errorf("cross-host redirect: %v", err)
	}
	if _, err := f.Fetch(ctx, "https://example.com/json"); !errors.Is(err, ErrNoCard) {
		t.Errorf("json: %v", err)
	}
	if _, err := f.Fetch(ctx, "https://example.com/missing"); err == nil {
		t.Error("404 made a card")
	}
	if _, err := f.Fetch(ctx, "http://example.com/"); !errors.Is(err, ErrUnsafe) {
		t.Errorf("plain http: %v", err)
	}
}

func TestFetchRefusesPrivateAddresses(t *testing.T) {
	srv, _ := testServer(t)
	f := New()
	f.dialTo = srv.Listener.Addr().String() // 127.0.0.1
	if _, err := f.Fetch(context.Background(), "https://example.com/"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("loopback: %v", err)
	}
}

func TestClean(t *testing.T) {
	if got := clean("a\u202eb\x00c\n\n d", 50); got != "a b c d" {
		t.Errorf("clean = %q", got)
	}
	if got := clean(strings.Repeat("x", 130), maxTitle); len([]rune(got)) != maxTitle || !strings.HasSuffix(got, "…") {
		t.Errorf("truncate = %q", got)
	}
}
