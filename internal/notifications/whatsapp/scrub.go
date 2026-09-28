package whatsapp

import (
	"net/url"
	"sort"
	"strings"
)

// secretScrubber redacts configured WhatsApp credential material from
// provider-controlled text: access token, app secret, verify token, and
// the Bearer authorization form. Scrubbing always runs on the complete
// field BEFORE diagnostic bounding so credentials can never survive as
// truncated fragments.
type secretScrubber struct {
	replacer *strings.Replacer
}

// newSecretScrubber builds the redactor for the configured secrets. A
// nil scrubber is safe: scrub returns its input unchanged.
func newSecretScrubber(secrets ...string) *secretScrubber {
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
	for _, secret := range secrets {
		add(secret)
		add("Bearer " + secret)
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

// scrub redacts every configured pattern. Nil-safe.
func (s *secretScrubber) scrub(text string) string {
	if s == nil || s.replacer == nil {
		return text
	}
	return s.replacer.Replace(text)
}
