package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// GraphQL transport bounds (Phase 11).
const (
	// maxResponseBytes bounds GraphQL response/error bodies before
	// decoding. Order/product payloads are small and bounded upstream
	// (line items fetched with a fixed first: limit).
	maxResponseBytes = 1024 * 1024
	// maxRetryAfter bounds accepted Retry-After metadata.
	maxRetryAfter = time.Hour
)

// Client is the minimal Shopify GraphQL Admin transport: HTTPS only,
// X-Shopify-Access-Token in the header (never in URL or query), no
// redirects (credentials never cross origins), bounded request and
// response bodies, bounded timeouts, classified errors. GraphQL
// documents are static; dynamic values travel as variables only.
//
// No retry loop lives here: failures return classified to the caller.
type Client struct {
	http      *http.Client
	endpoint  string
	token     string
	userAgent string
	scrub     *secretScrubber
}

// newClient builds the transport over an injected *http.Client so tests
// can supply TLS test transports deterministically. Redirect and timeout
// policy are always enforced here, never inherited from the caller.
func newClient(endpoint, accessToken, clientSecret string, timeout time.Duration, transport *http.Client) *Client {
	httpClient := &http.Client{Timeout: timeout}
	if transport != nil {
		httpClient.Transport = transport.Transport
		httpClient.Jar = transport.Jar
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		http: httpClient, endpoint: endpoint, token: accessToken,
		userAgent: "moonlight-cloud-commerce/1.0",
		scrub:     newSecretScrubber(accessToken, clientSecret),
	}
}

// gqlRequest is the fixed GraphQL POST shape. Only variables carry
// dynamic values.
type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// gqlError is one top-level GraphQL error entry.
type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

// gqlCost mirrors the throttling metadata block used for bounded,
// deterministic retry hints only.
type gqlCost struct {
	RequestedQueryCost float64 `json:"requestedQueryCost"`
	ActualQueryCost    float64 `json:"actualQueryCost"`
	ThrottleStatus     struct {
		MaximumAvailable   float64 `json:"maximumAvailable"`
		CurrentlyAvailable float64 `json:"currentlyAvailable"`
		RestoreRate        float64 `json:"restoreRate"`
	} `json:"throttleStatus"`
}

type gqlEnvelope struct {
	Data       json.RawMessage `json:"data"`
	Errors     []gqlError      `json:"errors"`
	Extensions struct {
		Cost *gqlCost `json:"cost"`
	} `json:"extensions"`
}

// do performs one GraphQL operation. A 200 response does NOT imply
// success: top-level errors[], userErrors[] (handled by the caller on
// the decoded payload), missing data, and malformed JSON are separate
// failure layers. Context cancellation is preserved unwrapped.
func (c *Client) do(ctx context.Context, document string, variables map[string]any, out any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	payload, err := json.Marshal(gqlRequest{Query: document, Variables: variables})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("X-Shopify-Access-Token", c.token)

	response, err := c.http.Do(request)
	if err != nil {
		return classifyTransport(ctx, err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return commerce.TemporaryError("shopify response read failed")
	}
	if int64(len(raw)) > maxResponseBytes {
		return commerce.TemporaryError("shopify response exceeds body limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return c.classifyStatus(response.StatusCode, response.Header.Get("Retry-After"), raw)
	}
	var envelope gqlEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return commerce.TemporaryError("shopify malformed success response")
	}
	if len(envelope.Errors) > 0 {
		return c.classifyGraphQLErrors(envelope)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return commerce.TemporaryError("shopify success response missing data")
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return commerce.TemporaryError("shopify malformed success response")
		}
	}
	return nil
}

// classifyGraphQLErrors maps top-level GraphQL errors (HTTP 200) into
// the frozen taxonomy. THROTTLED is retryable with a bounded
// deterministic hint derived from throttle metadata when present.
func (c *Client) classifyGraphQLErrors(envelope gqlEnvelope) error {
	first := envelope.Errors[0]
	code := boundField(first.Extensions.Code, codeLimit)
	message := c.safeMessage(first.Message)
	switch code {
	case "THROTTLED":
		return commerce.RateLimitedError(message, throttleHint(envelope.Extensions.Cost))
	case "ACCESS_DENIED", "UNAUTHENTICATED", "FORBIDDEN":
		return commerce.AuthenticationError(message)
	default:
		// HTTP 200 with an error body is not a validated success; retry
		// with the same idempotent operation key is always safe.
		return commerce.TemporaryError(message)
	}
}

// throttleHint derives a bounded deterministic wait from official
// throttle metadata: ceil((requested - available) / restoreRate)
// clamped to [1s, maxRetryAfter]. Missing metadata yields no hint.
func throttleHint(cost *gqlCost) time.Duration {
	if cost == nil || cost.ThrottleStatus.RestoreRate <= 0 {
		return 0
	}
	needed := cost.RequestedQueryCost - cost.ThrottleStatus.CurrentlyAvailable
	if needed < 1 {
		needed = 1
	}
	seconds := math.Ceil(needed / cost.ThrottleStatus.RestoreRate)
	if seconds < 1 {
		seconds = 1
	}
	if seconds > maxRetryAfter.Seconds() {
		seconds = maxRetryAfter.Seconds()
	}
	return time.Duration(seconds) * time.Second
}

// safeMessage bounds and scrubs one complete remote-controlled message.
// Scrubbing runs on the complete field before truncation so credentials
// sliced at a boundary can never leak in fragment form.
func (c *Client) safeMessage(message string) string {
	cleaned := boundField(c.scrub.scrub(message), messageLimit)
	if cleaned == "" {
		return "shopify request failed"
	}
	return "shopify error: " + cleaned
}

// classifyStatus maps non-2xx HTTP status to the frozen taxonomy.
// Redirects never reach here: the no-follow policy refuses them at the
// transport layer before any credential is forwarded.
func (c *Client) classifyStatus(status int, retryAfter string, raw []byte) error {
	message := c.safeMessage(graphQLErrorMessage(raw))
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return commerce.AuthenticationError(message)
	case status == http.StatusTooManyRequests:
		return commerce.RateLimitedError(message, parseRetryAfter(retryAfter))
	case status == http.StatusNotFound || status == http.StatusConflict:
		return commerce.ConflictError(message)
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return commerce.ValidationError(message)
	case status >= 500:
		return commerce.TemporaryError(message)
	default:
		return commerce.TemporaryError(message)
	}
}

// graphQLErrorMessage extracts the first top-level error message from a
// non-2xx body best-effort; unparseable bodies yield empty text.
func graphQLErrorMessage(raw []byte) string {
	var envelope struct {
		Errors []gqlError `json:"errors"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil || len(envelope.Errors) == 0 {
		return ""
	}
	return envelope.Errors[0].Message
}

// classifyTransport maps transport failures. Caller cancellation is
// preserved; refused redirects (no-follow policy), timeouts, and network
// failures are temporary: the same operation key retries safely.
func classifyTransport(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, http.ErrUseLastResponse) {
		return commerce.TemporaryError("shopify refused redirect")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return commerce.TemporaryError("shopify request timeout")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return commerce.TemporaryError("shopify transport failure")
}

// parseRetryAfter accepts delta-seconds or HTTP dates, bounded. Garbage
// never crashes and never yields absurd waits.
func parseRetryAfter(header string) time.Duration {
	value := strings.TrimSpace(header)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		wait := time.Duration(seconds) * time.Second
		if wait > maxRetryAfter {
			return maxRetryAfter
		}
		return wait
	}
	if when, err := http.ParseTime(value); err == nil {
		wait := time.Until(when)
		if wait < 0 {
			return 0
		}
		if wait > maxRetryAfter {
			return maxRetryAfter
		}
		return wait
	}
	return 0
}
