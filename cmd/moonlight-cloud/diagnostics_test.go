package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSupportBundleContainsNoSecrets is the §34 negative test: the
// support bundle must never become a credential-exfiltration channel.
// The bundle is built by allowlist, so this asserts both that the known
// secret field names are absent from the JSON and that the redaction
// note is present.
func TestSupportBundleContainsNoSecrets(t *testing.T) {
	bundle := SupportBundle{
		App:           "moonlight-cloud",
		Version:       "test",
		Commit:        "abc123",
		SchemaVersion: 43,
		TargetSchema:  43,
		DatabaseOK:    true,
		Projection: []ProjectionSummary{{
			Processor: "sale_projection.v1", Pending: 3, Blocked: 1,
			LastErrorCode: "SYNC_FAILED_INTERNAL",
		}},
		Environment: EnvironmentSummary{
			Environment: "production", DeviceControl: true, MFAKeyConfigured: true,
		},
		RedactionNote: "Allowlisted fields only.",
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	// Forbidden field names must never appear as JSON keys.
	for _, forbidden := range []string{
		"password", "passwd", "secret", "token", "authorization",
		"bearer", "private_key", "mfa_secret", "recovery_code",
		"credential", "api_key", "apikey", "session_id", "csrf",
	} {
		if strings.Contains(strings.ToLower(text), `"`+forbidden+`":`) {
			t.Fatalf("support bundle leaks a forbidden key %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "redaction_note") {
		t.Fatal("support bundle must carry its redaction note")
	}
	// The bundle must not embed raw SQL, connection strings or paths.
	for _, forbidden := range []string{"postgres://", "SELECT ", "INSERT ", "/var/", "/home/"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("support bundle leaks %q", forbidden)
		}
	}
}

// TestSupportBundleEnvRecordsBooleansOnly proves environment reporting
// records presence, never values.
func TestSupportBundleEnvRecordsBooleansOnly(t *testing.T) {
	env := EnvironmentSummary{
		Environment: "production", MFAKeyConfigured: true,
		ReleaseTrustLoaded: true,
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	// Base64 key material would look like a long quoted string; assert
	// the encoded struct has only scalar booleans/strings by checking
	// that no value exceeds a short length.
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for key, value := range decoded {
		if s, ok := value.(string); ok && len(s) > 32 {
			t.Fatalf("environment field %q carries too much data: %q", key, s)
		}
	}
	_ = text
}
