package woocommerce

import (
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// Boundary-test credentials. Distinctive synthetic values shared by the
// whole matrix to minimize false positives; filler runs never contain
// these characters.
const (
	boundaryKey    = "ZXQ9-KEY-BOUNDARY-12"
	boundarySecret = "ZXQ9-SECRET-BOUNDARY-UNIQUE-123456"
)

func boundaryClient() *Client {
	return newClient("https://store.example.com", boundaryKey, boundarySecret, 0, nil)
}

// assertBoundarySafe proves neither the complete credential nor the
// fragment the OLD truncate-then-scrub order would have leaked appears
// in output, plus the canonical short prefixes from the review.
func assertBoundarySafe(t *testing.T, what, output, secret string, start, limit int) {
	t.Helper()
	if strings.Contains(output, secret) {
		t.Fatalf("%s leaks complete credential in: %q", what, output)
	}
	if start < limit && start+len(secret) > limit {
		if fragment := secret[:limit-start]; strings.Contains(output, fragment) {
			t.Fatalf("%s leaks boundary fragment %q in: %q", what, fragment, output)
		}
	}
	for _, prefix := range boundaryPrefixes(secret) {
		if strings.Contains(output, prefix) {
			t.Fatalf("%s leaks prefix %q in: %q", what, prefix, output)
		}
	}
}

func boundaryPrefixes(secret string) []string {
	var out []string
	for _, length := range []int{5, 12, 20} {
		if length < len(secret) {
			out = append(out, secret[:length])
		}
	}
	return out
}

func TestWooMessageSecretBoundary(t *testing.T) {
	client := boundaryClient()
	limit, secret := messageLimit, boundarySecret
	offsets := []int{
		limit - len(secret) - 1,
		limit - len(secret),
		limit - len(secret) + 1,
		limit - 5,
		limit - 4,
		limit - 1,
		limit,
		limit + 1,
	}
	for _, start := range offsets {
		t.Run(strconv.Itoa(start), func(t *testing.T) {
			message := strings.Repeat("x", start) + secret
			text := client.safeWooMessage(&wooErrorResponse{Code: "test_code", Message: message})
			assertBoundarySafe(t, "message", text, secret, start, limit)
		})
	}
}

func TestWooMessageKeyBoundary(t *testing.T) {
	client := boundaryClient()
	limit, key := messageLimit, boundaryKey
	for _, start := range []int{limit - len(key) - 1, limit - len(key), limit - len(key) + 1, limit - 5, limit - 1, limit, limit + 1} {
		t.Run(strconv.Itoa(start), func(t *testing.T) {
			message := strings.Repeat("x", start) + key
			text := client.safeWooMessage(&wooErrorResponse{Code: "test_code", Message: message})
			assertBoundarySafe(t, "key", text, key, start, limit)
		})
	}
}

func TestWooCodeSecretBoundary(t *testing.T) {
	client := boundaryClient()
	limit, secret := codeLimit, boundarySecret
	for _, start := range []int{
		limit - len(secret) - 1,
		limit - len(secret),
		limit - len(secret) + 1,
		limit - 5,
		limit - 4,
		limit - 1,
		limit,
		limit + 1,
	} {
		t.Run(strconv.Itoa(start), func(t *testing.T) {
			code := strings.Repeat("y", start) + secret
			text := client.safeWooMessage(&wooErrorResponse{Code: code, Message: "m"})
			assertBoundarySafe(t, "code", text, secret, start, limit)
		})
	}
}

func TestWooBasicTokenBoundary(t *testing.T) {
	client := boundaryClient()
	token := base64.StdEncoding.EncodeToString([]byte(boundaryKey + ":" + boundarySecret))
	for _, form := range []string{token, "Basic " + token} {
		for _, start := range []int{100, messageLimit - len(form) + 1, messageLimit - 5, messageLimit - 1, messageLimit} {
			t.Run(strconv.Itoa(start), func(t *testing.T) {
				message := strings.Repeat("x", start) + form
				text := client.safeWooMessage(&wooErrorResponse{Code: "test_code", Message: message})
				assertBoundarySafe(t, "token", text, form, start, messageLimit)
				// Composing credentials never survive via another form.
				assertBoundarySafe(t, "token/key", text, boundaryKey, start, messageLimit)
				assertBoundarySafe(t, "token/secret", text, boundarySecret, start, messageLimit)
			})
		}
	}
}

func TestWooEscapedSecretBoundary(t *testing.T) {
	secret := "ZXQ9 SEC/RET?=+X"
	escaped := url.QueryEscape(secret)
	if escaped == secret {
		t.Fatal("secret must exercise escaping")
	}
	client := newClient("https://store.example.com", boundaryKey, secret, 0, nil)
	for _, start := range []int{messageLimit - len(escaped) - 1, messageLimit - len(escaped) + 1, messageLimit - 5, messageLimit - 1, messageLimit} {
		t.Run(strconv.Itoa(start), func(t *testing.T) {
			message := strings.Repeat("x", start) + escaped
			text := client.safeWooMessage(&wooErrorResponse{Code: "test_code", Message: message})
			assertBoundarySafe(t, "escaped", text, escaped, start, messageLimit)
			if strings.Contains(text, secret) {
				t.Fatalf("raw secret leaks in: %q", text)
			}
		})
	}
}

func TestWooLongestFirstBoundary(t *testing.T) {
	key, secret := "ZXQ9-KEY", "ZXQ9-KEY-LONGER-SECRET"
	client := newClient("https://store.example.com", key, secret, 0, nil)
	// Secret crossing the boundary: longest match wins, no partial
	// longer secret (and no orphan key fragment from it) remains.
	start := messageLimit - 5
	message := strings.Repeat("x", start) + secret
	text := client.safeWooMessage(&wooErrorResponse{Code: "test_code", Message: message})
	assertBoundarySafe(t, "longest", text, secret, start, messageLimit)
	if strings.Contains(text, key) {
		t.Fatalf("orphan key fragment remains in: %q", text)
	}
	// Fully interior secret redacts completely too.
	interior := client.safeWooMessage(&wooErrorResponse{Code: "test_code", Message: "see " + secret + " here"})
	if strings.Contains(interior, secret) || strings.Contains(interior, key) {
		t.Fatalf("interior leak in: %q", interior)
	}
}

func TestWooMultipleCredentialsBoundary(t *testing.T) {
	client := boundaryClient()
	token := base64.StdEncoding.EncodeToString([]byte(boundaryKey + ":" + boundarySecret))
	message := "prefix " + boundaryKey + strings.Repeat("x", 150) + boundarySecret +
		strings.Repeat("y", 10) + token + " suffix"
	text := client.safeWooMessage(&wooErrorResponse{Code: "test_code", Message: message})
	for _, secret := range []string{boundaryKey, boundarySecret, token, "Basic " + token} {
		if strings.Contains(text, secret) {
			t.Fatalf("leaks %q in: %q", secret, text)
		}
	}
	if len(text) > len("woo error test_code: ")+messageLimit+64 {
		t.Fatalf("diagnostic unbounded: %q", text)
	}
}

func TestWooRepeatedCredentialBoundary(t *testing.T) {
	client := boundaryClient()
	message := boundarySecret + " " + boundarySecret + " " + boundarySecret
	text := client.safeWooMessage(&wooErrorResponse{Code: "test_code", Message: message})
	if strings.Contains(text, boundarySecret) {
		t.Fatalf("repeated leak in: %q", text)
	}
	if got := strings.Count(text, "[redacted]"); got != 3 {
		t.Fatalf("all occurrences redacted, got %d in: %q", got, text)
	}
}

func TestWooDiagnosticUsefulness(t *testing.T) {
	client := boundaryClient()
	text := client.safeWooMessage(&wooErrorResponse{Code: "woocommerce_rest_invalid_product", Message: "Invalid product."})
	want := "woo error woocommerce_rest_invalid_product: Invalid product."
	if text != want {
		t.Fatalf("safe diagnostic changed: %q", text)
	}
	// Bounds still apply after redaction.
	long := client.safeWooMessage(&wooErrorResponse{
		Code:    strings.Repeat("c", 200),
		Message: strings.Repeat("m", 500),
	})
	if !strings.HasPrefix(long, "woo error "+strings.Repeat("c", codeLimit)+": ") {
		t.Fatalf("code bound: %q", long)
	}
}
