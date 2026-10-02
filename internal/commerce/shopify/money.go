package shopify

import (
	"fmt"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// Exact money conversion between MoonLight int64 minor units and
// Shopify decimal strings. Integer arithmetic only: no floats anywhere,
// values beyond 2^53 preserved exactly.

// FormatMinorUnits renders exact minor units as a Shopify decimal money
// string with two fraction digits: 65000 -> "650.00",
// 9007199254740993 -> "90071992547409.93". Negative amounts are refused
// (provider prices and quantities are non-negative); overflow cannot
// occur because int64 covers 19 digits and the formatting is string
// assembly.
func FormatMinorUnits(amountMinor int64) (string, error) {
	if amountMinor < 0 {
		return "", fmt.Errorf("money amount must not be negative")
	}
	units := amountMinor / 100
	cents := amountMinor % 100
	return fmt.Sprintf("%d.%02d", units, cents), nil
}

// ParseMoneyString converts a Shopify decimal money string into exact
// int64 minor units using the frozen generic parser (integer arithmetic,
// overflow- and precision-checked). Negative values are refused.
func ParseMoneyString(value, currency string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("empty money value")
	}
	minor, err := orders.ParseMinorUnits(value, currency)
	if err != nil {
		return 0, err
	}
	if minor < 0 {
		return 0, fmt.Errorf("money amount must not be negative")
	}
	return minor, nil
}
