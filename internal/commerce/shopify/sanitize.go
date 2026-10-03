package shopify

import (
	"net/url"
	"sort"
	"strings"
)

// secretScrubber redacts configured credential material from
// operator-visible text. Shopify error messages and GraphQL userErrors
// are remote-controlled input: a hostile or compromised shop can reflect
// secrets back (including inside truncation boundaries), so every
// remote-derived message passes through here before it can reach
// ProviderError strings, CLI output, logs, or wrapped errors.
//
// Protected forms: the raw access token and client secret, their
// URL-escaped and percent-decoded equivalents, and the bearer header
// form. Replacement is a fixed token; matching is longest-first so
// overlapping forms redact deterministically.
type secretScrubber struct {
	replacer *strings.Replacer
}

func newSecretScrubber(accessToken, clientSecret string) *secretScrubber {
	var patterns []string
	add := func(value string) {
		if value == "" {
			return
		}
		patterns = append(patterns, value)
		if escaped := url.QueryEscape(value); escaped != value {
			patterns = append(patterns, escaped)
		}
		if decoded, err := url.QueryUnescape(value); err == nil && decoded != value {
			patterns = append(patterns, decoded)
		}
		patterns = append(patterns, "Bearer "+value)
	}
	add(accessToken)
	add(clientSecret)
	sort.Slice(patterns, func(i, j int) bool { return len(patterns[i]) > len(patterns[j]) })
	seen := map[string]bool{}
	pairs := make([]string, 0, 2*len(patterns))
	for _, pattern := range patterns {
		if seen[pattern] {
			continue
		}
		seen[pattern] = true
		pairs = append(pairs, pattern, "[redacted]")
	}
	if len(pairs) == 0 {
		return nil
	}
	return &secretScrubber{replacer: strings.NewReplacer(pairs...)}
}

func (s *secretScrubber) scrub(text string) string {
	if s == nil || text == "" {
		return text
	}
	return s.replacer.Replace(text)
}

// Diagnostic output bounds (operator-output formatting, applied after
// redaction so credentials never survive truncation).
const (
	codeLimit    = 64
	messageLimit = 200
)

func boundField(field string, limit int) string {
	return truncateASCII(field, limit)
}

// truncateASCII strips control characters and truncates on a rune
// boundary: multi-byte UTF-8 is never split mid-character.
func truncateASCII(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}
