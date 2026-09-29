package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// checkServer fakes the three Kroger endpoints CheckKroger touches. A
// credential whose ID is "bad" is refused a token; only location "01400338"
// exists.
func checkServer(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if id, _, _ := r.BasicAuth(); id == "bad-client" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"access_token":"tok","expires_in":1800}`))
	})
	mux.HandleFunc("/locations/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/01400338") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"data":{"name":"Kroger Test","address":{"addressLine1":"1 Main St","city":"Cincinnati","state":"OH","zipCode":"45202"}}}`))
	})
	mux.HandleFunc("/products", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter.locationId") != "01400338" {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Write([]byte(`{"data":[{"items":[{"price":{"regular":3.49},"size":"1 gal"}]}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	oldTok, oldProd, oldLoc := krogerTokenURL, krogerProductURL, krogerLocationURL
	krogerTokenURL = srv.URL + "/token"
	krogerProductURL = srv.URL + "/products"
	krogerLocationURL = srv.URL + "/locations"
	t.Cleanup(func() { krogerTokenURL, krogerProductURL, krogerLocationURL = oldTok, oldProd, oldLoc })
}

func TestCheckKroger_AllGood(t *testing.T) {
	checkServer(t)
	res := CheckKroger(context.Background(), [][2]string{{"good-client", "s"}}, "01400338")
	if !res.OK {
		t.Fatalf("want OK, got %+v", res)
	}
	if res.StoreName != "Kroger Test" || res.StoreAddress != "1 Main St, Cincinnati, OH 45202" {
		t.Errorf("store = %q / %q", res.StoreName, res.StoreAddress)
	}
	if res.SamplePrice != 349 {
		t.Errorf("sample price = %d, want 349", res.SamplePrice)
	}
}

func TestCheckKroger_BadLocation(t *testing.T) {
	checkServer(t)
	res := CheckKroger(context.Background(), [][2]string{{"good-client", "s"}}, "99999999")
	if res.OK || res.LocationError == "" || res.SampleError == "" {
		t.Fatalf("want location + sample errors, got %+v", res)
	}
}

func TestCheckKroger_BadCredentialReported(t *testing.T) {
	checkServer(t)
	res := CheckKroger(context.Background(), [][2]string{{"bad-client", "s"}, {"good-client", "s"}}, "01400338")
	if res.OK {
		t.Fatal("one rejected credential should fail the check")
	}
	if len(res.Credentials) != 2 || res.Credentials[0].OK || !res.Credentials[1].OK {
		t.Fatalf("credentials = %+v", res.Credentials)
	}
	if res.StoreName == "" {
		t.Error("the good credential should still check the location")
	}
}

func TestCheckKroger_NoCredentials(t *testing.T) {
	res := CheckKroger(context.Background(), nil, "01400338")
	if res.OK || len(res.Credentials) != 1 || res.Credentials[0].Error == "" {
		t.Fatalf("got %+v", res)
	}
}
