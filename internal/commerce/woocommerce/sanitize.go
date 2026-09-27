package woocommerce

import (
	"encoding/base64"
	"net/url"
	"sort"
	"strings"
)

// secretScrubber redacts configured credential material from
// operator-visible text. Woo response fields are remote-controlled
// input: a hostile or compromised server can reflect secrets back, so
// every remote-derived error message passes through here before it can
// reach ProviderError strings, CLI output, logs, or wrapped errors.
//
// Protected forms: raw key/secret, URL-escaped and percent-decoded
// equivalents, and the Basic Authorization value (raw and header form).
// Replacement is a fixed token; matching is longest-first so overlapping
// forms redact deterministically.
type secretScrubber struct {
	replacer *strings.Replacer
}

// newSecretScrubber builds the redactor for one credential pair. A nil
// scrubber is safe: scrub returns its input unchanged.
func newSecretScrubber(key, secret string) *secretScrubber {
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
	}
	add(key)
	add(secret)
	if key != "" && secret != "" {
		token := base64.StdEncoding.EncodeToString([]byte(key + ":" + secret))
		patterns = append(patterns, token, "Basic "+token)
	}
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

// scrub redacts every protected credential form in text.
func (s *secretScrubber) scrub(text string) string {
	if s == nil || text == "" {
		return text
	}
	return s.replacer.Replace(text)
}
