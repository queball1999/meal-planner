// Package items handles catalog-item image storage. Images live on disk under
// ITEM_IMAGE_DIR (served at /item-images/{name}); the DB only holds the
// filename, mirroring the recipe image pipeline in package recipes.
package items

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"goeat/safefetch"
	"goeat/scrape"
)

const maxImageBytes = 8 * 1024 * 1024

// DownloadImage fetches imageURL into imageDir and returns the stored filename.
// The direct fetch goes through safefetch, so it is SSRF-guarded and
// size-capped; a private or unreachable host yields an error without ever
// trying render (FlareSolverr manages its own SSRF-safe egress separately).
//
// render is consulted only as a fallback: if the direct fetch's response
// isn't actually image bytes (a challenge or redirect page substituted for
// the real file - the telltale sign freefoodphotos.com or a similar host is
// blocking this request), one retry is made through FlareSolverr, on the
// theory that whatever anti-bot check rejected the plain request will pass
// its cleared browser. A zero-value render (nothing configured) just skips
// that retry and returns the original error.
func DownloadImage(ctx context.Context, imageURL, imageDir string, render scrape.RenderConfig) (string, error) {
	if imageDir == "" {
		return "", fmt.Errorf("items: ITEM_IMAGE_DIR is not configured")
	}
	data, contentType, err := fetchImageBytes(ctx, imageURL)
	if err != nil || !looksLikeImage(contentType, data) {
		if render.Enabled() {
			if flareData, flareCT, ferr := fetchImageViaFlareSolverr(ctx, imageURL, render); ferr == nil && looksLikeImage(flareCT, flareData) {
				data, contentType, err = flareData, flareCT, nil
			} else if err == nil {
				err = fmt.Errorf("response was not an image (got %q) and FlareSolverr fallback failed: %v", contentType, ferr)
			}
		} else if err == nil {
			err = fmt.Errorf("response was not an image (got %q)", contentType)
		}
	}
	if err != nil {
		return "", fmt.Errorf("items: fetch %s: %w", imageURL, err)
	}

	name := uuid.NewString() + imageExt(imageURL, contentType)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(imageDir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// fetchImageBytes is DownloadImage's direct-fetch path: a same-origin
// Referer, and an Accept that actually asks for an image (safefetch's own
// defaults are written for fetching HTML pages, and a bare image request
// with no Referer is exactly what a site's hotlink protection -
// freefoodphotos.com's seed photos included - is there to block).
func fetchImageBytes(ctx context.Context, imageURL string) (data []byte, contentType string, err error) {
	headers := map[string]string{
		"Accept": "image/avif,image/webp,image/apng,image/*,*/*;q=0.8",
	}
	if u, perr := url.Parse(imageURL); perr == nil && u.Scheme != "" && u.Host != "" {
		headers["Referer"] = u.Scheme + "://" + u.Host + "/"
	}
	res, err := safefetch.Fetch(ctx, imageURL, &safefetch.Options{MaxBytes: maxImageBytes, Headers: headers})
	if err != nil {
		return nil, "", err
	}
	return res.Body, res.ContentType, nil
}

// fetchImageViaFlareSolverr retries imageURL through FlareSolverr when the
// direct fetch looks blocked. FlareSolverr's own response is a real HTTP
// response with headers, so a non-image Content-Type here is just as
// diagnostic as it is on the direct path.
func fetchImageViaFlareSolverr(ctx context.Context, imageURL string, render scrape.RenderConfig) ([]byte, string, error) {
	var flare scrape.Renderer
	for _, r := range render.Renderers() {
		if r.Backend == scrape.RendererFlareSolverr {
			flare = r
			break
		}
	}
	if !flare.Enabled() {
		return nil, "", fmt.Errorf("no FlareSolverr renderer configured")
	}
	res, err := scrape.FetchViaProxy(ctx, imageURL, flare.URL)
	if err != nil {
		return nil, "", err
	}
	// FlareSolverr hands back its solution.response as text, which for a
	// binary image is whatever the underlying transport decoded it as
	// (typically Latin-1/ISO-8859-1, byte-for-byte) - re-encoding through
	// that same single-byte charset recovers the original bytes exactly,
	// unlike a UTF-8 round trip which would corrupt any byte outside ASCII.
	return latin1Bytes(res.HTML), http.DetectContentType([]byte(res.HTML)), nil
}

func latin1Bytes(s string) []byte {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			r = '?'
		}
		b = append(b, byte(r))
	}
	return b
}

// looksLikeImage reports whether a fetched response is actually image bytes
// rather than an HTML challenge/redirect page substituted for the real file.
func looksLikeImage(contentType string, data []byte) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if strings.HasPrefix(ct, "image/") {
		return true
	}
	if len(data) == 0 {
		return false
	}
	sniffed := http.DetectContentType(data)
	return strings.HasPrefix(sniffed, "image/")
}

// SaveImageBytes writes uploaded image bytes to imageDir under a fresh name.
func SaveImageBytes(imageDir, origName string, data []byte) (string, error) {
	if imageDir == "" {
		return "", fmt.Errorf("items: ITEM_IMAGE_DIR is not configured")
	}
	ext := strings.ToLower(filepath.Ext(origName))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
	default:
		return "", fmt.Errorf("items: unsupported image type %q", ext)
	}
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return "", err
	}
	name := uuid.NewString() + ext
	if err := os.WriteFile(filepath.Join(imageDir, name), data, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// RemoveImage deletes a stored image file, ignoring a missing file and
// rejecting any name that tries to escape imageDir.
func RemoveImage(imageDir, name string) {
	if imageDir == "" || name == "" || strings.ContainsAny(name, `/\`) {
		return
	}
	_ = os.Remove(filepath.Join(imageDir, name))
}

func imageExt(rawURL, contentType string) string {
	ct := strings.ToLower(strings.Split(contentType, ";")[0])
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	if u, err := url.Parse(rawURL); err == nil {
		if ext := filepath.Ext(u.Path); ext != "" {
			return ext
		}
	}
	return ".jpg"
}
