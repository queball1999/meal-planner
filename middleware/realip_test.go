package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// TestRealIP covers QSS security design §5.4: X-Forwarded-For is believed
// only from a trusted proxy, and then read right to left.
func TestRealIP(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}

	cases := []struct {
		name    string
		trusted []netip.Prefix
		peer    string
		xff     []string
		want    string
	}{
		{"no proxies configured: header ignored", nil, "203.0.113.9:5000", []string{"1.2.3.4"}, "203.0.113.9"},
		{"untrusted peer: header ignored", proxies, "203.0.113.9:5000", []string{"1.2.3.4"}, "203.0.113.9"},
		{"trusted peer: client from header", proxies, "172.18.0.2:5000", []string{"198.51.100.7"}, "198.51.100.7"},
		{"spoofed leftmost entry is skipped", proxies, "172.18.0.2:5000", []string{"6.6.6.6, 198.51.100.7"}, "198.51.100.7"},
		{"proxy chain is walked past", proxies, "172.18.0.2:5000", []string{"198.51.100.7, 172.20.0.5"}, "198.51.100.7"},
		{"multiple header lines", proxies, "172.18.0.2:5000", []string{"6.6.6.6", "198.51.100.7"}, "198.51.100.7"},
		{"trusted peer, no header", proxies, "172.18.0.2:5000", nil, "172.18.0.2"},
		{"garbage in header keeps the peer", proxies, "172.18.0.2:5000", []string{"not-an-ip"}, "172.18.0.2"},
		{"ipv6 peer", nil, "[::1]:5000", []string{"1.2.3.4"}, "::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			h := RealIP(tc.trusted)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = ClientIP(r)
			}))
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			if got != tc.want {
				t.Errorf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
