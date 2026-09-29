package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"strings"

	"goeat/middleware"
)

// ── Security headers (QSS security design §10) ──────────────────────────────

type nonceKey struct{}

// cspNonceFrom returns the request's CSP nonce ("" outside securityHeaders).
func cspNonceFrom(r *http.Request) string {
	n, _ := r.Context().Value(nonceKey{}).(string)
	return n
}

// securityHeaders sets the CSP and the other browser-hardening headers on
// every response. Inline <script> blocks run only with this request's nonce
// (templates render it with {{cspNonce}}), so injected markup can't run
// script. Inline event handlers (onclick=) are refused outright - wire them
// with addEventListener instead.
//
// style-src keeps 'unsafe-inline': the templates use style="" attributes
// throughout, and CSS injection can't run script (§10).
//
// https is PUBLIC_BASE_URL being https, which turns on HSTS.
func securityHeaders(https bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Hex, not base64: html/template would entity-escape base64's "+".
		nonce := hex.EncodeToString(b)

		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; "+
			"script-src 'self' 'nonce-"+nonce+"'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; frame-src 'self'; "+
			"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// camera=(self): the barcode scanner (barcode-camera.js).
		h.Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=()")
		if https {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nonceKey{}, nonce)))
	})
}

// noStoreSignedIn keeps signed-in pages out of the browser cache, so the back
// button after sign-out (or the next person at a shared computer) can't bring
// them back. Static assets stay cacheable. Runs inside LoadSession.
func noStoreSignedIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if middleware.UserFromCtx(r) != nil && !strings.HasPrefix(r.URL.Path, "/static/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// ── Host allowlist (QSS security design §9.3) ───────────────────────────────

// hostGuard answers 421 to a request whose Host is a name not on allowed (the
// PUBLIC_BASE_URL host plus ALLOWED_HOSTS). Any website a user visits can
// send requests to a LAN address, and with DNS rebinding read the answers.
// A rebinding page's requests carry the attacker's hostname, never an IP
// literal, so IP addresses and localhost are always accepted - opening the
// server as http://192.168.1.20:8080 keeps working - and only names are
// checked. /health stays open for the container healthcheck.
//
// An empty list turns the check off (with a startup warning), so an install
// that never set PUBLIC_BASE_URL keeps working until it does.
func hostGuard(allowed []string, next http.Handler) http.Handler {
	if len(allowed) == 0 {
		log.Printf("WARNING: PUBLIC_BASE_URL and ALLOWED_HOSTS are unset - Host names aren't checked, which leaves the server open to DNS rebinding. Set PUBLIC_BASE_URL (and ALLOWED_HOSTS for any other name you use).")
		return next
	}
	set := make(map[string]bool, len(allowed))
	for _, h := range allowed {
		set[strings.ToLower(h)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" && !hostAllowed(set, r.Host) {
			http.Error(w, "Misdirected Request - this server doesn't answer to that host name. Add it to ALLOWED_HOSTS.", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed: an IP literal or localhost (any port), or a listed host -
// matched with its port, or without one when the entry has none.
func hostAllowed(set map[string]bool, host string) bool {
	host = strings.ToLower(host)
	if set[host] {
		return true
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.Trim(name, "[]")
	if name == "localhost" || net.ParseIP(name) != nil {
		return true
	}
	return set[name]
}
