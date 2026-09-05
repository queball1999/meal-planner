// Package scrape provides server-side HTML fetching and product extraction
// for the configurable scraping engine (§6.7).
package scrape

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxBodyBytes = 512 * 1024 // 512 KB cap
	fetchTimeout = 10 * time.Second
)

var defaultClient = &http.Client{Timeout: fetchTimeout}

// FetchResult is the raw HTML returned by Fetch or FetchViaProxy.
type FetchResult struct {
	HTML       string
	FinalURL   string
	StatusCode int
}

// Fetch performs a plain GET with a size cap and timeout. Returns the HTML body.
func Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; GoEat/1.0; +https://github.com/go-eat)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := defaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch: status %d from %s", resp.StatusCode, rawURL)
	}

	return &FetchResult{
		HTML:       string(body),
		FinalURL:   resp.Request.URL.String(),
		StatusCode: resp.StatusCode,
	}, nil
}

// FetchViaProxy routes the request through a FlareSolverr instance when the
// direct fetch returns a challenge page (403/503). proxyURL is the FlareSolverr
// base URL, e.g. "http://localhost:8191".
func FetchViaProxy(ctx context.Context, targetURL, proxyURL string) (*FetchResult, error) {
	payload := map[string]any{
		"cmd":           "request.get",
		"url":           targetURL,
		"maxTimeout":    15000,
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		proxyURL+"/v1", strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Status   string `json:"status"`
		Solution struct {
			URL     string `json:"url"`
			Response string `json:"response"`
			Status  int    `json:"status"`
		} `json:"solution"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&out); err != nil {
		return nil, err
	}
	if out.Status != "ok" {
		return nil, fmt.Errorf("flaresolverr: status=%q", out.Status)
	}
	return &FetchResult{
		HTML:       out.Solution.Response,
		FinalURL:   out.Solution.URL,
		StatusCode: out.Solution.Status,
	}, nil
}
