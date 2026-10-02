package shopify

import (
	"errors"
	"strconv"
	"strings"
)

// errInvalidTime is the internal parse failure for malformed provider
// timestamps. It never surfaces directly: callers wrap it in typed
// blocked/provider errors.
var errInvalidTime = errors.New("invalid shopify timestamp")

// errInvalidDomain is the internal failure for non-canonical shop
// domains.
var errInvalidDomain = errors.New("invalid shopify shop domain")

// errInvalidVersion is the internal failure for unpinned API versions.
var errInvalidVersion = errors.New("invalid shopify api version")

// bounded strips control characters and truncates remote-controlled
// text for normalized snapshot fields.
func bounded(value string, limit int) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if len(cleaned) > limit {
		cleaned = cleaned[:limit]
	}
	return cleaned
}

// parseInt64 parses a canonical decimal resource id into int64.
func parseInt64(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, errInvalidTime
	}
	return parsed, nil
}
