package shopify

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// Phase 11 §16/§17/§201: a hostile Shopify error reflecting the
// configured access token or client secret must be completely redacted
// from every operator-visible error — at the beginning, middle, end, and
// across truncation boundaries.

func TestSecretReflectionIsRedacted(t *testing.T) {
	token := testConfig().AccessToken
	secret := testConfig().ClientSecret
	messages := map[string]string{
		"token at beginning":  token + " rejected",
		"token in middle":     "request used " + token + " somehow",
		"token at end":        "bad credential " + token,
		"secret at beginning": secret + " leaked",
		"secret in middle":    "hmac key " + secret + " wrong",
		"secret at end":       "signature from " + secret,
		// Long enough that truncation would slice the credential if
		// redaction ran after bounding.
		"secret across truncation boundary": strings.Repeat("x", 190) + secret + strings.Repeat("y", 190),
		"url escaped":                       url.QueryEscape(token),
		"bearer form":                       "Bearer " + token,
	}
	for name, message := range messages {
		h := newHarness(t)
		provider := newTestProvider(t, h)
		body := `{"errors":[{"message":` + jsonString(message) + `}]}`
		h.setFailure("MoonlightProduct", 500, body)
		_, err := provider.loadProduct(context.Background(), "gid://shopify/Product/1")
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		text := err.Error()
		if strings.Contains(text, token) {
			t.Fatalf("%s: access token leaked: %q", name, text)
		}
		if strings.Contains(text, secret) {
			t.Fatalf("%s: client secret leaked: %q", name, text)
		}
		if strings.Contains(text, "Bearer "+token) {
			t.Fatalf("%s: bearer form leaked: %q", name, text)
		}
	}
}

// Phase 11 §200: userError messages are bounded and scrubbed the same
// way as top-level errors.
func TestUserErrorReflectionIsRedacted(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	secret := testConfig().ClientSecret
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	h.setFailure("MoonlightInventorySet", 200,
		`{"data":{"inventorySetQuantities":{"inventoryAdjustmentGroup":null,"userErrors":[{"code":"INVALID","message":`+
			jsonString("rejected "+secret)+`}]}}}`)
	req := inventoryRequest(created.ExternalProductID, "prod-1", 5, true)
	req.OperationKey = "inv-redact"
	err = provider.SetInventory(context.Background(), req)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("client secret leaked through userErrors: %v", err)
	}
}

func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
