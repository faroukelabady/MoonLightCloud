package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// maxResponseBytes bounds Graph response/error bodies before decoding.
const maxResponseBytes = 1024 * 1024

// maxRetryAfter bounds accepted Retry-After metadata.
const maxRetryAfter = time.Hour

// maxMessageIDLen bounds accepted provider message IDs.
const maxMessageIDLen = 256

// Provider is the WhatsApp Cloud API template sender. Template-only in
// v1: no free-form text, no media, no interactivity. Construction
// validates configuration eagerly and performs no network I/O.
type Provider struct {
	key           notifications.ProviderKey
	graphVersion  string
	baseURL       string
	phoneNumberID string
	accessToken   string
	http          *http.Client
	scrub         *secretScrubber
}

// NewProvider builds the adapter from validated configuration. A nil
// httpClient gets production transport with redirect refusal and the
// configured timeout; tests inject a TLS stub transport.
func NewProvider(cfg config.WhatsAppNotificationConfig, httpClient *http.Client) (*Provider, error) {
	key, err := notifications.ValidateProviderKey(cfg.ProviderKey)
	if err != nil {
		return nil, err
	}
	if cfg.AccessToken == "" || cfg.PhoneNumberID == "" {
		return nil, fmt.Errorf("whatsapp provider requires phone number ID and access token")
	}
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	if client.Timeout <= 0 {
		client.Timeout = config.DefaultWhatsAppHTTPTimeout
	}
	if httpClient != nil {
		client.Transport = httpClient.Transport
		client.Jar = httpClient.Jar
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Provider{
		key: key, graphVersion: cfg.GraphVersion,
		baseURL: cfg.NormalizedBaseURL(), phoneNumberID: cfg.PhoneNumberID,
		accessToken: cfg.AccessToken, http: client,
		scrub: newSecretScrubber(cfg.AccessToken, cfg.AppSecret, cfg.WebhookVerifyToken),
	}, nil
}

// Key returns the logical provider instance key, never the vendor name.
func (p *Provider) Key() notifications.ProviderKey {
	return p.key
}

// templateParameter is one Meta BODY text parameter.
type templateParameter struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// templateComponent is the BODY component carrying ordered parameters.
type templateComponent struct {
	Type       string              `json:"type"`
	Parameters []templateParameter `json:"parameters,omitempty"`
}

// templateRef is the Meta template reference.
type templateRef struct {
	Name     string `json:"name"`
	Language struct {
		Code string `json:"code"`
	} `json:"language"`
	Components []templateComponent `json:"components,omitempty"`
}

// templateRequest is the exact Graph send shape.
type templateRequest struct {
	MessagingProduct string      `json:"messaging_product"`
	RecipientType    string      `json:"recipient_type"`
	To               string      `json:"to"`
	Type             string      `json:"type"`
	Template         templateRef `json:"template"`
}

// sendResponse is the Graph success envelope. messages[] entries carry
// an id plus a message_status that must read accepted: held or paused
// messages were NOT accepted and never count as success.
type sendResponse struct {
	Messages []struct {
		ID            string `json:"id"`
		MessageStatus string `json:"message_status"`
	} `json:"messages"`
}

// SendTemplate delivers one template message. The request resolves the
// enqueue-time snapshot (never live mapping state). Success requires
// exactly one accepted provider message ID; every other outcome is a
// classified error, with unknown remote outcomes as Ambiguous.
func (p *Provider) SendTemplate(ctx context.Context, req notifications.TemplateSendRequest) (notifications.SendResult, error) {
	if ctx.Err() != nil {
		return notifications.SendResult{}, ctx.Err()
	}
	body, err := p.renderRequest(req)
	if err != nil {
		return notifications.SendResult{}, err
	}
	target := p.baseURL + "/" + p.graphVersion + "/" + p.phoneNumberID + "/messages"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return notifications.SendResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+p.accessToken)
	request.Header.Set("User-Agent", "moonlight-cloud-notifications/1.0")
	var wroteRequest bool
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { wroteRequest = true },
	}
	request = request.WithContext(httptrace.WithClientTrace(ctx, trace))
	response, err := p.http.Do(request)
	if err != nil {
		return notifications.SendResult{}, classifyTransport(ctx, err, wroteRequest)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return notifications.SendResult{}, notifications.AmbiguousError("whatsapp response read failed")
	}
	if int64(len(raw)) > maxResponseBytes {
		return notifications.SendResult{}, notifications.AmbiguousError("whatsapp response exceeds body limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return notifications.SendResult{}, p.classifyStatus(response.StatusCode,
			response.Header.Get("Retry-After"), raw)
	}
	return p.acceptResponse(raw)
}

// renderRequest builds the exact Graph body from the snapshotted
// resolution. Parameter values travel verbatim as JSON text.
func (p *Provider) renderRequest(req notifications.TemplateSendRequest) ([]byte, error) {
	if req.ProviderKey != p.key {
		return nil, notifications.ValidationError("template request addressed to another provider instance")
	}
	if err := notifications.ValidateRecipient(req.Recipient); err != nil {
		return nil, notifications.ValidationError(err.Error())
	}
	resolved := req.Resolved
	if resolved.ExternalTemplateName == "" || resolved.ExternalLanguageCode == "" {
		return nil, notifications.ValidationError("template mapping snapshot incomplete")
	}
	parameters := make([]templateParameter, 0, len(resolved.ParameterOrder))
	for _, name := range resolved.ParameterOrder {
		value, ok := resolved.Parameters[name]
		if !ok {
			return nil, notifications.ValidationError("template parameter missing")
		}
		parameters = append(parameters, templateParameter{Type: "text", Text: value})
	}
	out := templateRequest{
		MessagingProduct: "whatsapp", RecipientType: "individual",
		To: req.Recipient, Type: "template",
	}
	out.Template.Name = resolved.ExternalTemplateName
	out.Template.Language.Code = resolved.ExternalLanguageCode
	if len(parameters) > 0 {
		out.Template.Components = []templateComponent{{Type: "body", Parameters: parameters}}
	}
	return json.Marshal(out)
}

// acceptResponse enforces the strict single-ID success invariant: a 2xx
// without exactly one accepted non-empty bounded message ID is
// AMBIGUOUS, because Meta may already have accepted the send.
func (p *Provider) acceptResponse(raw []byte) (notifications.SendResult, error) {
	var parsed sendResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&parsed); err != nil {
		return notifications.SendResult{}, notifications.AmbiguousError("whatsapp success response malformed")
	}
	if len(parsed.Messages) != 1 {
		return notifications.SendResult{}, notifications.AmbiguousError("whatsapp success without exactly one message")
	}
	entry := parsed.Messages[0]
	if entry.MessageStatus != "" && entry.MessageStatus != "accepted" {
		return notifications.SendResult{}, notifications.ValidationError(
			"whatsapp message not accepted")
	}
	if !validMessageID(entry.ID) {
		return notifications.SendResult{}, notifications.AmbiguousError("whatsapp success without provider message ID")
	}
	return notifications.SendResult{ProviderMessageID: entry.ID}, nil
}

// validMessageID bounds accepted provider IDs. No literal wamid prefix
// is required: only non-empty printable bounded content.
func validMessageID(id string) bool {
	if len(id) == 0 || len(id) > maxMessageIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 32 || id[i] == 127 {
			return false
		}
	}
	return true
}

// graphError is Meta's standard error envelope, best-effort decoded.
type graphError struct {
	Error struct {
		Code    json.Number `json:"code"`
		Message string      `json:"message"`
		Type    string      `json:"type"`
	} `json:"error"`
}

// classifyStatus maps HTTP status to the notification taxonomy. The
// safe message is scrubbed BEFORE bounding (Phase 6B lesson).
func (p *Provider) classifyStatus(status int, retryAfter string, raw []byte) *notifications.NotificationError {
	message := p.safeGraphMessage(raw)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return notifications.AuthenticationError(message)
	case status == http.StatusRequestTimeout:
		return notifications.TemporaryError(message)
	case status == http.StatusTooManyRequests:
		return notifications.RateLimitedError(message, parseRetryAfter(retryAfter))
	case status == http.StatusConflict:
		return notifications.ConflictError(message)
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return notifications.ValidationError(message)
	case status >= 500:
		return notifications.TemporaryError(message)
	default:
		return notifications.TemporaryError(message)
	}
}

// classifyTransport separates proven before-write failures (safe retry)
// from possibly-written ones (ambiguous). Caller cancellation is
// preserved unwrapped.
func classifyTransport(ctx context.Context, err error, wroteRequest bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !wroteRequest {
		return notifications.TemporaryError("whatsapp request never written")
	}
	return notifications.AmbiguousError("whatsapp request outcome unknown")
}

// safeGraphMessage extracts the bounded code/message pair with
// credential scrubbing applied FIRST on the complete decoded field.
func (p *Provider) safeGraphMessage(raw []byte) string {
	var parsed graphError
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&parsed); err != nil {
		return "whatsapp request failed"
	}
	code := boundField(p.scrub.scrub(parsed.Error.Code.String()))
	message := boundField(p.scrub.scrub(parsed.Error.Message))
	var text string
	if code == "" && message == "" {
		text = "whatsapp request failed"
	} else if code == "" {
		text = "whatsapp error: " + message
	} else {
		text = "whatsapp error " + code + ": " + message
	}
	return text
}

// boundField strips control content and truncates to the diagnostic
// limit. Never called before scrubbing.
func boundField(field string) string {
	const limit = 200
	field = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, field)
	field = strings.TrimSpace(field)
	if len(field) > limit {
		field = field[:limit]
	}
	return field
}

// parseRetryAfter bounds Retry-After metadata to the backoff ceiling.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := parsePositiveInt(value); err == nil {
		if seconds > int64((maxRetryAfter / time.Second)) {
			return maxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := time.Until(at)
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

func parsePositiveInt(value string) (int64, error) {
	var n int64
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int64(value[i]-'0')
		if n > 1<<40 {
			return 0, fmt.Errorf("too large")
		}
	}
	return n, nil
}
