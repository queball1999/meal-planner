// Package safefetch is the one SSRF-guarded HTTP fetch helper for the app
// (§16.1). Every server-side external fetch must go through this package.
// It blocks private/loopback/link-local IPs, re-checks on every redirect
// hop, and enforces size and timeout bounds.
package safefetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// Result holds the response from a safe fetch.
type Result struct {
	Body       []byte
	FinalURL   string
	StatusCode int
	// ContentType is the raw Content-Type header value.
	ContentType string
}

// Options controls fetch behaviour. Zero values use the defaults below.
type Options struct {
	MaxBytes     int64         // default 512 KB
	Timeout      time.Duration // default 10 s
	MaxRedirects int           // default 3

	// Headers are set on the request, overriding the defaults. Retail sites
	// serve a bot wall to anything that does not look like a browser.
	Headers map[string]string

	// CookieJar keeps cookies across redirect hops. Store sites hand out a
	// session cookie on the first hop and 403 the redirect target without it,
	// so a jar is the difference between a product page and a challenge page.
	CookieJar bool

	// KeepErrorBody returns the response instead of an error on a 4xx/5xx, so
	// the caller can inspect a challenge page rather than being told only that
	// it got a 403.
	KeepErrorBody bool
}

const (
	defaultMaxBytes     = 512 * 1024
	defaultTimeout      = 10 * time.Second
	defaultMaxRedirects = 3
)

var errBlockedIP = errors.New("safefetch: resolved IP is in a blocked range")

// Fetch performs an SSRF-guarded GET. Scheme must be http or https.
// Private, loopback, link-local, and multicast IPs are rejected before
// dialing and on every redirect hop.
func Fetch(ctx context.Context, rawURL string, opts *Options) (*Result, error) {
	if opts == nil {
		opts = &Options{}
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxRedirects := opts.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = defaultMaxRedirects
	}

	var jar http.CookieJar
	if opts.CookieJar {
		jar, _ = cookiejar.New(nil)
	}

	client := &http.Client{
		Timeout: timeout,
		Jar:     jar,
		Transport: &http.Transport{
			DialContext: ssrfDialer(),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("safefetch: stopped after %d redirects", maxRedirects)
			}
			// Re-check the redirect destination.
			if err := checkHost(req.URL.Hostname()); err != nil {
				return err
			}
			return nil
		},
	}

	// Pre-check before dialing.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("safefetch: %w", err)
	}
	scheme := req.URL.Scheme
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("safefetch: scheme %q not allowed", scheme)
	}
	if err := checkHost(req.URL.Hostname()); err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; GoEat/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("safefetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("safefetch: read body: %w", err)
	}
	if resp.StatusCode >= 400 && !opts.KeepErrorBody {
		return nil, fmt.Errorf("safefetch: HTTP %d from %s", resp.StatusCode, rawURL)
	}

	return &Result{
		Body:        body,
		FinalURL:    resp.Request.URL.String(),
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// ssrfDialer returns a DialContext that resolves the host and rejects
// any address in a blocked IP range before opening the connection.
func ssrfDialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 5 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, raw := range ips {
			ip := net.ParseIP(raw)
			if ip == nil {
				continue
			}
			if isBlocked(ip) {
				return nil, errBlockedIP
			}
		}
		// Dial the first resolved IP directly (pin it - no re-resolve).
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
	}
}

// checkHost resolves hostname and returns errBlockedIP if any resolved
// address falls in a blocked range. Used for pre-dial and redirect checks.
func checkHost(hostname string) error {
	if hostname == "" {
		return fmt.Errorf("safefetch: empty hostname")
	}
	// If it parses as an IP directly, check it immediately.
	if ip := net.ParseIP(hostname); ip != nil {
		if isBlocked(ip) {
			return errBlockedIP
		}
		return nil
	}
	ips, err := net.LookupHost(hostname)
	if err != nil {
		return fmt.Errorf("safefetch: resolve %q: %w", hostname, err)
	}
	for _, raw := range ips {
		if ip := net.ParseIP(raw); ip != nil && isBlocked(ip) {
			return errBlockedIP
		}
	}
	return nil
}

var (
	// RFC 1918 private ranges.
	private10    = mustCIDR("10.0.0.0/8")
	private172   = mustCIDR("172.16.0.0/12")
	private192   = mustCIDR("192.168.0.0/16")
	loopback4    = mustCIDR("127.0.0.0/8")
	loopback6    = mustCIDR("::1/128")
	linkLocal4   = mustCIDR("169.254.0.0/16")
	linkLocal6   = mustCIDR("fe80::/10")
	uniqueLocal  = mustCIDR("fc00::/7")
	multicast4   = mustCIDR("224.0.0.0/4")
	multicast6   = mustCIDR("ff00::/8")
	unspecified4 = mustCIDR("0.0.0.0/8")
)

var blockedRanges = []*net.IPNet{
	private10, private172, private192,
	loopback4, loopback6,
	linkLocal4, linkLocal6,
	uniqueLocal,
	multicast4, multicast6,
	unspecified4,
}

func isBlocked(ip net.IP) bool {
	for _, cidr := range blockedRanges {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}
