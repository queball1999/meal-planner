package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	krogerTokenURL   = "https://api.kroger.com/v1/connect/oauth2/token"
	krogerProductURL = "https://api.kroger.com/v1/products"
	krogerTimeout    = 10 * time.Second
)

// KrogerProvider wraps the Kroger Products API (§6.2 OfficialAPIProvider).
// OAuth2 client-credentials tokens are cached in memory and refreshed on expiry.
type KrogerProvider struct {
	clientID     string
	clientSecret string
	locationID   string
	client       *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewKrogerProvider constructs the Kroger adapter. Returns nil when no credentials
// are configured - the chain skips nil providers at construction time.
func NewKrogerProvider(clientID, clientSecret, locationID string) *KrogerProvider {
	if clientID == "" || clientSecret == "" {
		return nil
	}
	return &KrogerProvider{
		clientID:     clientID,
		clientSecret: clientSecret,
		locationID:   locationID,
		client:       &http.Client{Timeout: krogerTimeout},
	}
}

func (k *KrogerProvider) Name() string { return "kroger" }

func (k *KrogerProvider) Lookup(ctx context.Context, term string, _ int64, _ string) (*PriceResult, error) {
	tok, err := k.getToken(ctx)
	if err != nil {
		log.Printf("kroger: token error: %v", err)
		return nil, nil // fail soft
	}

	q := url.Values{}
	q.Set("filter.term", term)
	q.Set("filter.fulfillment", "ais")
	if k.locationID != "" {
		q.Set("filter.locationId", k.locationID)
	}
	q.Set("filter.limit", "5")

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		krogerProductURL+"?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")

	resp, err := k.client.Do(req)
	if err != nil {
		log.Printf("kroger: request error: %v", err)
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("kroger: status %d for %q", resp.StatusCode, term)
		return nil, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))

	var payload struct {
		Data []struct {
			Description string `json:"description"`
			Items       []struct {
				Price struct {
					Regular float64 `json:"regular"`
				} `json:"price"`
				Size string `json:"size"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Data) == 0 {
		return nil, nil
	}

	// Pick the first product that has a non-zero price.
	for _, prod := range payload.Data {
		if len(prod.Items) == 0 {
			continue
		}
		item := prod.Items[0]
		if item.Price.Regular <= 0 {
			continue
		}
		priceCents := int64(item.Price.Regular * 100)
		packSize, unit := parseSize(item.Size)
		return &PriceResult{
			PriceCents:   priceCents,
			PurchaseUnit: unit,
			PackSize:     packSize,
			Source:       "live",
			Confidence:   ConfidenceLive,
			FetchedAt:    time.Now().UTC(),
		}, nil
	}
	return nil, nil
}

// getToken returns a valid access token, fetching a new one when expired.
func (k *KrogerProvider) getToken(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.token != "" && time.Now().Before(k.tokenExp) {
		return k.token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("scope", "product.compact")

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, krogerTokenURL,
		strings.NewReader(form.Encode()))
	req.SetBasicAuth(k.clientID, k.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("kroger token: status %d", resp.StatusCode)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	k.token = tok.AccessToken
	k.tokenExp = time.Now().Add(time.Duration(tok.ExpiresIn-30) * time.Second)
	return k.token, nil
}

// parseSize attempts to extract a numeric pack size and unit from strings like
// "5 lb", "12 oz", "6 count". Returns (1, "each") when parsing fails.
func parseSize(s string) (float64, string) {
	s = strings.ToLower(strings.TrimSpace(s))
	var size float64
	var unit string
	_, err := fmt.Sscanf(s, "%f %s", &size, &unit)
	if err != nil || size <= 0 {
		return 1, "each"
	}
	// Normalize common unit aliases.
	switch unit {
	case "lbs", "pound", "pounds":
		unit = "lb"
	case "ounce", "ounces", "oz.":
		unit = "oz"
	case "count", "ct", "pk", "pack", "packs":
		unit = "each"
	}
	return size, unit
}
