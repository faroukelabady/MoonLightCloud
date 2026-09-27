package woocommerce

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// maxResponseBytes bounds Woo response/error bodies before decoding.
const maxResponseBytes = 1024 * 1024

// maxRetryAfter bounds accepted Retry-After metadata.
const maxRetryAfter = time.Hour

// Client is the minimal Woo REST transport: HTTPS only, Basic Auth in
// the header, no redirects (credentials never cross origins), bounded
// bodies and timeouts, classified errors. No retry loop: one bounded
// recovery lookup lives in the provider, everything else returns to the
// caller classified.
type Client struct {
	http      *http.Client
	baseURL   string
	key       string
	secret    string
	userAgent string
	scrub     *secretScrubber
}

// newClient builds the transport over an injected *http.Client so tests
// can supply TLS test transports deterministically. Redirect and timeout
// policy are always enforced here, never inherited from the caller.
func newClient(baseURL, key, secret string, timeout time.Duration, transport *http.Client) *Client {
	httpClient := &http.Client{Timeout: timeout}
	if transport != nil {
		httpClient.Transport = transport.Transport
		httpClient.Jar = transport.Jar
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		http: httpClient, baseURL: strings.TrimSuffix(baseURL, "/"),
		key: key, secret: secret, userAgent: "moonlight-cloud-commerce/1.0",
		scrub: newSecretScrubber(key, secret),
	}
}

// doToken performs one Woo request. Success (2xx) decodes into out when
// non-nil; failures return a classified *commerce.ProviderError. Context
// cancellation is preserved unwrapped; everything else is classified.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) (int, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	target := c.baseURL + "/wp-json/wc/v3" + path
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, err
	}
	request.SetBasicAuth(c.key, c.secret)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.userAgent)

	response, err := c.http.Do(request)
	if err != nil {
		return 0, classifyTransport(ctx, err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return response.StatusCode, commerce.TemporaryError("woo response read failed")
	}
	if int64(len(raw)) > maxResponseBytes {
		return response.StatusCode, commerce.TemporaryError("woo response exceeds body limit")
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if out != nil {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(out); err != nil {
				return response.StatusCode, commerce.TemporaryError("woo malformed success response")
			}
		}
		return response.StatusCode, nil
	}
	return response.StatusCode, c.classifyStatus(response.StatusCode, response.Header.Get("Retry-After"), parseWooError(raw))
}

// parseWooError decodes Woo's standard error shape best-effort. It never
// fails: unparseable bodies yield an empty code/message.
func parseWooError(raw []byte) *wooErrorResponse {
	var parsed wooErrorResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return &wooErrorResponse{}
	}
	return &parsed
}

// safeWooMessage bounds the Woo error code and message for the generic
// error, with credential scrubbing applied FIRST on the complete
// decoded field. Truncating before scrubbing would slice a credential
// into an unmatchable fragment that leaks to operator output; the HTTP
// response body is already globally bounded, so scrubbing the complete
// field is safe. Bodies, request payloads, and raw response text never
// enter here; only the bounded code/message pair does, and even those
// are remote-controlled.
func (c *Client) safeWooMessage(wooError *wooErrorResponse) string {
	code := boundField(c.scrubField(wooError.Code), codeLimit)
	message := boundField(c.scrubField(wooError.Message), messageLimit)
	var text string
	if code == "" && message == "" {
		text = "woo request failed"
	} else if code == "" {
		text = "woo error: " + message
	} else {
		text = "woo error " + code + ": " + message
	}
	return text
}

// scrubField redacts configured credential material from one complete
// remote field. Nil-safe: without credentials there is nothing to redact.
func (c *Client) scrubField(field string) string {
	if c == nil || c.scrub == nil {
		return field
	}
	return c.scrub.scrub(field)
}

// Diagnostic output bounds (operator-output formatting, applied after
// redaction).
const (
	codeLimit    = 64
	messageLimit = 200
)

// boundField strips unsafe control content and truncates to the
// diagnostic limit.
func boundField(field string, limit int) string {
	return truncateASCII(field, limit)
}

func truncateASCII(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

// classifyStatus maps HTTP status plus Woo error semantics to the frozen
// taxonomy. Redirects never reach here: the no-follow policy refuses
// them at the transport layer.
func (c *Client) classifyStatus(status int, retryAfter string, wooError *wooErrorResponse) *commerce.ProviderError {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return commerce.AuthenticationError(c.safeWooMessage(wooError))
	case status == http.StatusRequestTimeout:
		return commerce.TemporaryError(c.safeWooMessage(wooError))
	case status == http.StatusTooManyRequests:
		return commerce.RateLimitedError(c.safeWooMessage(wooError), parseRetryAfter(retryAfter))
	case status == http.StatusNotFound || status == http.StatusConflict:
		return commerce.ConflictError(c.safeWooMessage(wooError))
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		if wooError.Code == wooCodeDuplicateSKU {
			return commerce.ConflictError(c.safeWooMessage(wooError))
		}
		return commerce.ValidationError(c.safeWooMessage(wooError))
	case status >= 500:
		return commerce.TemporaryError(c.safeWooMessage(wooError))
	default:
		return commerce.TemporaryError(c.safeWooMessage(wooError))
	}
}

// classifyTransport maps transport failures. Caller cancellation is
// preserved; refused redirects (no-follow policy) and client timeouts
// and network failures are temporary.
func classifyTransport(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, http.ErrUseLastResponse) {
		return commerce.TemporaryError("woo refused redirect")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return commerce.TemporaryError("woo request timeout")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return commerce.TemporaryError("woo transport failure")
}

// parseRetryAfter accepts delta-seconds or HTTP dates, bounded. Garbage
// never crashes and never yields absurd waits.
func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil {
		if seconds < 0 || seconds > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := time.Parse(time.RFC1123, header); err == nil {
		delay := time.Until(date)
		if delay < 0 {
			return 0
		}
		if delay > maxRetryAfter {
			return maxRetryAfter
		}
		return delay
	}
	return 0
}
