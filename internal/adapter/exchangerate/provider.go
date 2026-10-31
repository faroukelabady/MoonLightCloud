// Package exchangerate reads the USD->EGP rate from an open.er-api.com
// style endpoint (ADR-0058), the provider MoonLightRetail uses. It is the
// only code that knows the provider's wire format; settings.Fetcher sees a
// settings.RateSource.
package exchangerate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxBody = 1 << 20
	// A rate is plain decimal text; longer text or an exponent is refused
	// before math/big reads it.
	maxRateText = 32
	timeout     = 10 * time.Second
)

// ErrProvider reports an unusable provider answer; the stored rate stays.
var ErrProvider = errors.New("exchange rate provider returned no usable USD->EGP rate")

// Provider fetches one URL that answers {"result":"success","base_code":
// "USD","rates":{"EGP":…}}.
type Provider struct {
	url    string
	host   string
	client *http.Client
}

// New builds a Provider for an absolute http(s) URL. A nil client gets a
// 10-second timeout and does not follow redirects.
func New(rawURL string, client *http.Client) (*Provider, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return nil, fmt.Errorf("exchange rate URL must be an absolute http(s) URL")
	}
	if client == nil {
		client = &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Provider{url: rawURL, host: strings.ToLower(u.Hostname()), client: client}, nil
}

// Name is the provider host, such as "open.er-api.com".
func (p *Provider) Name() string { return p.host }

type response struct {
	Result   string                 `json:"result"`
	BaseCode string                 `json:"base_code"`
	Rates    map[string]json.Number `json:"rates"`
}

// USDToEGP returns the provider's rate in millionths, rounded half up from
// the decimal text (never through a float).
func (p *Provider) USDToEGP(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%w: http %d", ErrProvider, resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxBody))
	decoder.UseNumber()
	var body response
	if err := decoder.Decode(&body); err != nil {
		return 0, fmt.Errorf("%w: body", ErrProvider)
	}
	number, ok := body.Rates["EGP"]
	if body.Result != "success" || body.BaseCode != "USD" || !ok {
		return 0, ErrProvider
	}
	micro, ok := microrate(number.String())
	if !ok {
		return 0, ErrProvider
	}
	return micro, nil
}

// microrate reads positive decimal text ("48.7123456") as millionths.
func microrate(text string) (int64, bool) {
	if text == "" || len(text) > maxRateText || strings.ContainsAny(text, "eE+-") {
		return 0, false
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok || r.Sign() <= 0 {
		return 0, false
	}
	r.Mul(r, big.NewRat(1_000_000, 1))
	// Half up: floor(r + 1/2) for a positive r.
	r.Add(r, big.NewRat(1, 2))
	n := new(big.Int).Quo(r.Num(), r.Denom())
	if !n.IsInt64() || n.Sign() <= 0 {
		return 0, false
	}
	return n.Int64(), true
}
