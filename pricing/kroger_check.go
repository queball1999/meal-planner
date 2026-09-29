package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// krogerLocationURL is a package var so tests can point it at an httptest
// server, like the endpoints in kroger.go.
var krogerLocationURL = "https://api.kroger.com/v1/locations"

// krogerCheckTerm is the product searched for by CheckKroger. Every Kroger
// store stocks milk, so an empty result points at the location, not the term.
const krogerCheckTerm = "milk"

// KrogerCredCheck is the token-exchange outcome for one client ID.
type KrogerCredCheck struct {
	ClientID string `json:"client_id"` // masked
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// KrogerCheck is the result of CheckKroger, shaped for the Settings page's
// Test connection button.
type KrogerCheck struct {
	OK          bool              `json:"ok"`
	Credentials []KrogerCredCheck `json:"credentials"`

	LocationID    string `json:"location_id"`
	StoreName     string `json:"store_name,omitempty"`
	StoreAddress  string `json:"store_address,omitempty"`
	LocationError string `json:"location_error,omitempty"`

	SampleTerm  string `json:"sample_term"`
	SamplePrice int64  `json:"sample_price_cents,omitempty"`
	SampleError string `json:"sample_error,omitempty"`
}

// CheckKroger verifies a Kroger configuration end to end: every credential
// can fetch a token, the location ID names a real store, and a product search
// at that store returns a price. Unlike Lookup it never fails soft - every
// problem is reported so the operator can fix it.
func CheckKroger(ctx context.Context, creds [][2]string, locationID string) KrogerCheck {
	res := KrogerCheck{LocationID: locationID, SampleTerm: krogerCheckTerm}

	k := NewKrogerProvider(creds, locationID, 0, 0)
	if k == nil {
		res.Credentials = []KrogerCredCheck{{Error: "No Client ID / Client secret pair is set."}}
		return res
	}

	var token string
	for _, c := range k.creds {
		cc := KrogerCredCheck{ClientID: maskID(c.id)}
		tok, err := k.getToken(ctx, c)
		if err != nil {
			cc.Error = krogerErrText(err)
		} else {
			cc.OK = true
			if token == "" {
				token = tok
			}
		}
		res.Credentials = append(res.Credentials, cc)
	}
	if token == "" {
		return res
	}

	if locationID == "" {
		res.LocationError = "Location ID is not set - prices will not be store-specific."
	} else if err := k.checkLocation(ctx, token, &res); err != nil {
		res.LocationError = krogerErrText(err)
	}

	p, _, err := k.searchProducts(ctx, krogerCheckTerm, token)
	switch {
	case err != nil:
		res.SampleError = krogerErrText(err)
	case p == nil:
		res.SampleError = "The search worked but returned no priced products. " +
			"Check the Location ID - prices only come back for a store that sells online."
	default:
		res.SamplePrice = p.PriceCents
	}

	res.OK = res.LocationError == "" && res.SampleError == ""
	for _, c := range res.Credentials {
		res.OK = res.OK && c.OK
	}
	return res
}

// checkLocation looks up the store behind k.locationID and fills in its name
// and address.
func (k *KrogerProvider) checkLocation(ctx context.Context, token string, res *KrogerCheck) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		krogerLocationURL+"/"+url.PathEscape(k.locationID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	k.limiter.Wait()
	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return fmt.Errorf("Kroger has no store with location ID %q", k.locationID)
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &krogerHTTPError{status: resp.StatusCode, body: string(snippet)}
	}

	var payload struct {
		Data struct {
			Name    string `json:"name"`
			Address struct {
				Line1   string `json:"addressLine1"`
				City    string `json:"city"`
				State   string `json:"state"`
				ZipCode string `json:"zipCode"`
			} `json:"address"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&payload); err != nil {
		return fmt.Errorf("could not read the location response: %w", err)
	}
	res.StoreName = payload.Data.Name
	a := payload.Data.Address
	var parts []string
	for _, s := range []string{a.Line1, a.City, strings.TrimSpace(a.State + " " + a.ZipCode)} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	res.StoreAddress = strings.Join(parts, ", ")
	return nil
}

// krogerErrText turns an error from the Kroger client into a sentence an
// operator can act on.
func krogerErrText(err error) string {
	he, ok := err.(*krogerHTTPError)
	if !ok {
		return err.Error()
	}
	switch he.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("Kroger rejected the credentials (HTTP %d). Check the Client ID and secret.", he.status)
	case http.StatusTooManyRequests:
		return "Rate-limited by Kroger (HTTP 429). Try again in a minute."
	}
	return he.Error()
}
