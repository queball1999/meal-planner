package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goeat/safefetch"
)

// Store logos are downloaded once from Google's favicon service and cached on
// disk beside the item images, the same way catalog-item photos are: the CSP
// only allows same-origin images, and a household should not ping Google on
// every page view. Only catalog domains are fetched, so the route cannot be
// used as an open proxy.

var (
	storeLogoMu     sync.Mutex         // one download at a time; logos are tiny
	storeLogoFailed sync.Map           // domain -> time.Time of last failure
	storeLogoRetry  = 30 * time.Minute // how long a failed domain is left alone
	storeLogoSource = "https://www.google.com/s2/favicons?domain=%s&sz=64"
)

// storeLogoFallback is drawn when a logo cannot be fetched: the same mdi
// "store" glyph the templates use, so the chip never shows a broken image.
var storeLogoFallback = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="64" height="64">` +
	`<path fill="#9aa0a6" d="` + mdiPaths["store"] + `"/></svg>`

// storeLogoDir is where cached logos live: store-logos/ next to the item image
// directory. "" (item images disabled) means no disk cache.
func (s *Server) storeLogoDir() string {
	if s.itemImageDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Clean(s.itemImageDir)), "store-logos")
}

// handleStoreLogo serves /store-logos/{domain}, downloading and caching the
// logo on first request.
func (s *Server) handleStoreLogo(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if !isCatalogDomain(domain) {
		http.NotFound(w, r)
		return
	}
	if path := s.cachedStoreLogo(r.Context(), domain); path != "" {
		w.Header().Set("Cache-Control", "public, max-age=604800")
		http.ServeFile(w, r, path)
		return
	}
	// Short cache so a fixed network shows the real logo soon.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(storeLogoFallback))
}

// cachedStoreLogo returns the on-disk path of domain's logo, fetching it first
// if needed. "" when it is not cached and cannot be fetched right now.
func (s *Server) cachedStoreLogo(ctx context.Context, domain string) string {
	dir := s.storeLogoDir()
	if dir == "" {
		return ""
	}
	if p := findStoreLogo(dir, domain); p != "" {
		return p
	}
	if t, ok := storeLogoFailed.Load(domain); ok && time.Since(t.(time.Time)) < storeLogoRetry {
		return ""
	}

	storeLogoMu.Lock()
	defer storeLogoMu.Unlock()
	if p := findStoreLogo(dir, domain); p != "" { // another request got it
		return p
	}
	p, err := downloadStoreLogo(ctx, dir, domain)
	if err != nil {
		storeLogoFailed.Store(domain, time.Now())
		log.Printf("store logo: %s: %v", domain, err)
		return ""
	}
	storeLogoFailed.Delete(domain)
	return p
}

func findStoreLogo(dir, domain string) string {
	for _, ext := range []string{".png", ".ico", ".jpg", ".gif", ".webp", ".svg"} {
		p := filepath.Join(dir, domain+ext)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func downloadStoreLogo(ctx context.Context, dir, domain string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := safefetch.Fetch(ctx, fmt.Sprintf(storeLogoSource, domain), &safefetch.Options{
		MaxBytes: 256 * 1024,
		Headers:  map[string]string{"Accept": "image/*,*/*;q=0.8"},
	})
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", res.StatusCode)
	}
	ct := strings.ToLower(strings.SplitN(res.ContentType, ";", 2)[0])
	if !strings.HasPrefix(ct, "image/") {
		ct = http.DetectContentType(res.Body)
	}
	ext := map[string]string{
		"image/png": ".png", "image/x-icon": ".ico", "image/vnd.microsoft.icon": ".ico",
		"image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp", "image/svg+xml": ".svg",
	}[strings.TrimSpace(ct)]
	if ext == "" {
		return "", fmt.Errorf("not an image (%q)", ct)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, domain+ext)
	if err := os.WriteFile(path, res.Body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// isCatalogDomain reports whether domain belongs to a KnownStores entry.
func isCatalogDomain(domain string) bool {
	if domain == "" {
		return false
	}
	for _, d := range storeDomainMap {
		if d == domain {
			return true
		}
	}
	return false
}
