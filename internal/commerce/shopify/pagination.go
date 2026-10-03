package shopify

import (
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

const (
	maxConnectionPages = 40
	maxConnectionNodes = 2000
	paginationTimeout  = 2 * time.Minute
)

func paginationError() error {
	return commerce.TemporaryError("SHOPIFY_CONNECTION_INCOMPLETE")
}

func nextPage(info *pageInfo, count int, seen map[string]bool) (string, bool, error) {
	if info == nil || info.HasNextPage == nil {
		return "", false, paginationError()
	}
	if !*info.HasNextPage {
		return "", false, nil
	}
	if count == 0 || info.EndCursor == nil {
		return "", false, paginationError()
	}
	cursor := *info.EndCursor
	if cursor == "" || len(cursor) > 1024 || strings.TrimSpace(cursor) != cursor || strings.ContainsAny(cursor, "\r\n\x00") || seen[cursor] {
		return "", false, paginationError()
	}
	seen[cursor] = true
	return cursor, true, nil
}
