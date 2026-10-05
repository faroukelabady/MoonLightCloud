package shopify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
	version   string
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
		version:   strings.Split(strings.TrimSuffix(endpoint, "/graphql.json"), "/")[len(strings.Split(strings.TrimSuffix(endpoint, "/graphql.json"), "/"))-1],
		scrub:     newSecretScrubber(accessToken, clientSecret),
	}
}

// gqlRequest is the fixed GraphQL POST shape. Only variables carry
// dynamic values.
type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// gqlError is one top-level GraphQL error entry. Path is retained as raw
// evidence: its presence (in any form) marks field-level execution and
// disqualifies pre-execution-refusal settlement; malformed or
// contradictory path evidence fails closed the same way.
type gqlError struct {
	Message    string          `json:"message"`
	Path       json.RawMessage `json:"path"`
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
	if err := commerce.ProductSyncGuard(ctx); err != nil {
		return err
	}
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

	var token string
	mutation := strings.HasPrefix(strings.TrimSpace(document), "mutation ")
	if mutation {
		digest := sha256.Sum256(payload)
		token, err = commerce.BeginProductMutation(ctx, fmt.Sprintf("%x", digest))
		if err != nil {
			return err
		}
	}
	complete := func() error {
		if token == "" {
			return nil
		}
		return commerce.CompleteProductMutation(ctx, token)
	}
	if ctx.Err() != nil {
		if err := complete(); err != nil {
			return err
		}
		return ctx.Err()
	}
	var beforeWriteFailure, headersWritten atomic.Bool
	trace := &httptrace.ClientTrace{ConnectDone: func(_, _ string, e error) {
		if e != nil {
			beforeWriteFailure.Store(true)
		}
	}, TLSHandshakeDone: func(_ tls.ConnectionState, e error) {
		if e != nil {
			beforeWriteFailure.Store(true)
		}
	}, WroteHeaders: func() { headersWritten.Store(true) }}
	request = request.WithContext(httptrace.WithClientTrace(ctx, trace))
	response, err := c.http.Do(request)
	if err != nil {
		if beforeWriteFailure.Load() && !headersWritten.Load() {
			if releaseErr := complete(); releaseErr != nil {
				return releaseErr
			}
		}
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
		// These explicit refusal statuses establish no mutation was authorized.
		switch response.StatusCode {
		case 400, 401, 403, 404, 409, 422, 429:
			if err := complete(); err != nil {
				return err
			}
		}
		return c.classifyStatus(response.StatusCode, response.Header.Get("Retry-After"), raw)
	}
	if response.Header.Get("X-Shopify-API-Version") != c.version {
		return commerce.ValidationError("shopify served API version does not match supported pin")
	}
	var envelope gqlEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return commerce.TemporaryError("shopify malformed success response")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return commerce.TemporaryError("shopify malformed success response")
	}
	if len(envelope.Errors) > 0 {
		for _, e := range envelope.Errors {
			if e.Message == "" {
				return commerce.TemporaryError("shopify incomplete error response")
			}
		}
		// Error classification (what the caller receives) and settlement
		// (whether the remote mutation definitively did or did not apply)
		// are separate decisions. A retryable classification never
		// authorizes removing durable uncertainty evidence: the barrier is
		// released only when the COMPLETE response carries validated
		// evidence of a documented pre-execution refusal. Execution
		// evidence (any data key, any error path), unknown or missing
		// error codes, mixed arrays, incomplete messages and malformed
		// responses all leave the mutation uncertain and retain the
		// barrier.
		if mutation && definitiveGraphQLRefusal(envelope) {
			if err := complete(); err != nil {
				return err
			}
		}
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
	if mutation {
		if !mutationOutcomeComplete(document, envelope.Data) {
			return commerce.TemporaryError("shopify incomplete mutation outcome")
		}
		if document == docBundleCreate || document == docBundleUpdate {
			root, role := "productBundleCreate", "bundle_create"
			if document == docBundleUpdate {
				root, role = "productBundleUpdate", "bundle_update"
			}
			var data map[string]struct {
				Operation *struct {
					ID string `json:"id"`
				} `json:"productBundleOperation"`
				UserErrors []json.RawMessage `json:"userErrors"`
			}
			if err := json.Unmarshal(envelope.Data, &data); err != nil {
				return commerce.TemporaryError("shopify incomplete operation receipt")
			}
			result := data[root]
			if len(result.UserErrors) == 0 && result.Operation != nil {
				if _, err := ParseGID(result.Operation.ID, "ProductBundleOperation"); err != nil {
					return commerce.TemporaryError("shopify operation receipt identity invalid")
				}
				store, err := commerce.ProductAsyncReceipts(ctx)
				if err != nil {
					return err
				}
				digest := sha256.Sum256(payload)
				return store.AcknowledgeAsync(ctx, token, commerce.AsyncProductReceipt{
					Role: role, Intent: fmt.Sprintf("%x", digest), OperationID: result.Operation.ID,
				})
			}
		}
		if err := complete(); err != nil {
			return err
		}
	}
	return nil
}

// definitiveGraphQLRefusal reports whether the COMPLETE response is a
// definitive pre-execution refusal, so no remote mutation can have
// applied. Only the documented request-error shape qualifies (GraphQL
// request-error result contract: a request refused before execution
// carries errors and NO data key at all, and request errors carry no
// path):
//
//   - THROTTLED: cost-based admission rejects the request before
//     execution begins (the throttle bucket must hold the requested cost
//     before execution; throttled responses carry no actual query cost);
//   - ACCESS_DENIED / UNAUTHENTICATED / FORBIDDEN: authorization and
//     scope rejection — the operation is refused, not partially run.
//
// Settlement evidence rules — every one fails closed:
//
//   - any data key disqualifies, INCLUDING explicit `data: null`
//     (present data describes an execution result);
//   - any error path disqualifies (field-level execution evidence);
//     malformed or contradictory path evidence disqualifies too (the raw
//     path is retained and any presence is treated as evidence);
//   - every error entry must carry a message and one of the approved
//     codes — missing codes, incomplete messages and mixed uncertain
//     entries disqualify.
//
// A recognized code alone never settles; the caller's error
// classification (Authentication/Temporary/…) never settles either.
func definitiveGraphQLRefusal(envelope gqlEnvelope) bool {
	if len(envelope.Errors) == 0 {
		return false
	}
	if len(envelope.Data) > 0 {
		return false
	}
	for _, e := range envelope.Errors {
		if e.Message == "" || e.Extensions.Code == "" {
			return false
		}
		if len(e.Path) > 0 {
			return false
		}
		switch e.Extensions.Code {
		case "THROTTLED", "ACCESS_DENIED", "UNAUTHENTICATED", "FORBIDDEN":
		case "GRAPHQL_VALIDATION_FAILED":
			// Phase 15-R2 F06: documented pre-execution request
			// validation refusal — the document never ran, so there is no
			// remote side effect to remain uncertain about. Same strict
			// evidence standard: message + code, no data key, no paths.
		default:
			return false
		}
	}
	return true
}

// classifyGraphQLErrors maps top-level GraphQL errors (HTTP 200) into
// the frozen taxonomy for the caller. It never decides settlement: the
// durable mutation barrier is governed by definitiveGraphQLRefusal
// separately, so classification (including retryability) cannot erase
// uncertainty evidence. THROTTLED is retryable with a bounded
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
	case "GRAPHQL_VALIDATION_FAILED":
		// Deterministic pre-execution schema/contract refusal: bounded
		// permanent validation failure (F06), never a retry storm.
		return commerce.ValidationError(message)
	default:
		// HTTP 200 with an error body is not a validated success; the
		// mutation outcome is unknown and stays durably blocked.
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
	message = "shopify error: " + cleaned
	if len(message) > messageLimit {
		message = message[:messageLimit]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return message
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
// failures retain their error classification; uncertain mutations remain
// durably blocked independently of that retryability classification.
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
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return 0
		}
		if seconds >= int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
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
