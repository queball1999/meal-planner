package web

import (
	"net/http"
	"net/url"
	"strings"
)

// publicOrigin parses PUBLIC_BASE_URL into a scheme+host origin, or nil when
// it is unset or not an absolute http(s) URL.
func publicOrigin(raw string) *url.URL {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}
}

// csrfSchemeGuard rejects a state-changing request whose Origin (or, lacking
// one, Referer) names the configured public host but the other scheme -
// http://goeat.example posting to an https deployment. The CSRF check's
// Origin-vs-Host fallback can't see the scheme, so this sits in front of it
// (QSS security design §6.1 step 4). A no-op when PUBLIC_BASE_URL is unset.
func csrfSchemeGuard(public *url.URL, next http.Handler) http.Handler {
	if public == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		src := r.Header.Get("Origin")
		if src == "" || src == "null" {
			src = r.Header.Get("Referer")
		}
		if src != "" {
			if u, err := url.Parse(src); err == nil && strings.EqualFold(u.Host, public.Host) && u.Scheme != public.Scheme {
				http.Error(w, "Forbidden - cross-scheme request", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
