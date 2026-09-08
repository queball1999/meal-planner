// Package items handles catalog-item image storage. Images live on disk under
// ITEM_IMAGE_DIR (served at /item-images/{name}); the DB only holds the
// filename, mirroring the recipe image pipeline in package recipes.
package items

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"goeat/safefetch"
)

const maxImageBytes = 8 * 1024 * 1024

// DownloadImage fetches imageURL into imageDir and returns the stored filename.
// It goes through safefetch, so it is SSRF-guarded and size-capped; a private
// or unreachable host yields an error.
func DownloadImage(ctx context.Context, imageURL, imageDir string) (string, error) {
	if imageDir == "" {
		return "", fmt.Errorf("items: ITEM_IMAGE_DIR is not configured")
	}
	// A same-origin Referer, and an Accept that actually asks for an image:
	// safefetch's own defaults are written for fetching HTML pages, and a
	// bare image request with no Referer is exactly what a site's hotlink
	// protection (freefoodphotos.com's seed photos included) is there to
	// block.
	headers := map[string]string{
		"Accept": "image/avif,image/webp,image/apng,image/*,*/*;q=0.8",
	}
	if u, perr := url.Parse(imageURL); perr == nil && u.Scheme != "" && u.Host != "" {
		headers["Referer"] = u.Scheme + "://" + u.Host + "/"
	}
	res, err := safefetch.Fetch(ctx, imageURL, &safefetch.Options{MaxBytes: maxImageBytes, Headers: headers})
	if err != nil {
		return "", fmt.Errorf("items: fetch %s: %w", imageURL, err)
	}
	name := uuid.NewString() + imageExt(imageURL, res.ContentType)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(imageDir, name)
	if err := os.WriteFile(path, res.Body, 0o644); err != nil {
		return "", err
	}
	return name, nil
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
