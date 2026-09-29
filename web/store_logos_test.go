package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreLogoURLIsSameOrigin(t *testing.T) {
	if got := StoreLogoURL("Walmart"); got != "/store-logos/walmart.com" {
		t.Errorf("StoreLogoURL(Walmart) = %q", got)
	}
	if got := StoreLogoURL("Corner Market"); got != "" {
		t.Errorf("unknown store should have no logo, got %q", got)
	}
}

func serveLogo(s *Server, domain string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /store-logos/{domain}", s.handleStoreLogo)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/store-logos/"+domain, nil))
	return rec
}

func TestStoreLogoRejectsNonCatalogDomain(t *testing.T) {
	if rec := serveLogo(&Server{}, "evil.example"); rec.Code != http.StatusNotFound {
		t.Errorf("non-catalog domain: status %d, want 404", rec.Code)
	}
}

func TestStoreLogoServesCachedFile(t *testing.T) {
	root := t.TempDir()
	s := &Server{itemImageDir: filepath.Join(root, "item-images")}
	dir := s.storeLogoDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nfake")
	if err := os.WriteFile(filepath.Join(dir, "target.com.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := serveLogo(s, "target.com")
	if rec.Code != http.StatusOK || rec.Body.String() != string(png) {
		t.Errorf("cached logo: status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestStoreLogoFallbackWithoutCacheDir(t *testing.T) {
	rec := serveLogo(&Server{}, "costco.com")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "svg") {
		t.Errorf("fallback: status %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
