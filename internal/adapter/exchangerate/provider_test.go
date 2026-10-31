package exchangerate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, status int, body string) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("accept %q", r.Header.Get("Accept"))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	p, err := New(srv.URL+"/v6/latest/USD", nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProviderReadsTheRateExactly(t *testing.T) {
	for raw, want := range map[string]int64{
		"48.7123456": 48_712_346, // half up at the seventh decimal
		"48.7123454": 48_712_345,
		"50":         50_000_000,
		"49.25":      49_250_000,
	} {
		p := serve(t, 200, `{"result":"success","base_code":"USD","time_last_update_unix":1,"rates":{"USD":1,"EGP":`+raw+`}}`)
		got, err := p.USDToEGP(context.Background())
		if err != nil || got != want {
			t.Fatalf("%s = %d, %v; want %d", raw, got, err, want)
		}
	}
}

func TestProviderRefusesUnusableAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"http error":  {500, `{}`},
		"not success": {200, `{"result":"error","base_code":"USD","rates":{"EGP":50}}`},
		"other base":  {200, `{"result":"success","base_code":"EUR","rates":{"EGP":50}}`},
		"no EGP":      {200, `{"result":"success","base_code":"USD","rates":{"USD":1}}`},
		"negative":    {200, `{"result":"success","base_code":"USD","rates":{"EGP":-50}}`},
		"zero":        {200, `{"result":"success","base_code":"USD","rates":{"EGP":0}}`},
		"exponent":    {200, `{"result":"success","base_code":"USD","rates":{"EGP":5e999999999}}`},
		"long text":   {200, `{"result":"success","base_code":"USD","rates":{"EGP":50.` + strings.Repeat("1", 40) + `}}`},
		"not JSON":    {200, `<html>`},
	} {
		if _, err := serve(t, tc.status, tc.body).USDToEGP(context.Background()); !errors.Is(err, ErrProvider) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestProviderDoesNotFollowRedirects(t *testing.T) {
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.RedirectHandler(other.URL, http.StatusFound))
	t.Cleanup(srv.Close)
	p, err := New(srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.USDToEGP(context.Background()); !errors.Is(err, ErrProvider) || hit {
		t.Fatalf("redirect followed: hit=%v err=%v", hit, err)
	}
}

func TestNewNamesTheHostAndRefusesBadURLs(t *testing.T) {
	p, err := New("https://Open.ER-API.com:443/v6/latest/USD", nil)
	if err != nil || p.Name() != "open.er-api.com" {
		t.Fatalf("%v %v", p, err)
	}
	for _, raw := range []string{"", "open.er-api.com/v6", "ftp://x/y", "https:///path"} {
		if _, err := New(raw, nil); err == nil {
			t.Fatalf("%q accepted", raw)
		}
	}
}
