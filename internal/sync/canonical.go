// Exact canonical JSON numbers (Phase 2D HIGH-03).
//
// Semantic rule A: numerically equivalent JSON spellings canonicalize
// identically (1, 1.0, 1e0, 10e-1 all hash as "1"). No value passes through
// float64 or any IEEE-754 representation; normalization is exact decimal
// work on the lexical token with bounded expansion.
package sync

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Bounds keep hostile inputs from forcing enormous allocations. A single
// number token longer than this, or a decimal expansion beyond it, is
// rejected as malformed before ACK (deterministic safe behavior).
const (
	maxNumberTokenLen  = 10000
	maxCanonicalDigits = 10000
	maxExponentAbs     = 1000000
)

// normalizeNumber maps one JSON number token to its canonical decimal
// spelling. Integer values never round; distinct int64 values never collide;
// -0 normalizes to "0".
func normalizeNumber(s string) (string, error) {
	if len(s) == 0 || len(s) > maxNumberTokenLen {
		return "", fmt.Errorf("invalid JSON number")
	}
	i := 0
	neg := false
	if s[i] == '-' {
		neg = true
		i++
		if i >= len(s) {
			return "", fmt.Errorf("invalid JSON number")
		}
	}
	intStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	intPart := s[intStart:i]
	if len(intPart) == 0 {
		return "", fmt.Errorf("invalid JSON number")
	}
	if len(intPart) > 1 && intPart[0] == '0' {
		return "", fmt.Errorf("invalid JSON number: leading zeros")
	}
	fracPart := ""
	if i < len(s) && s[i] == '.' {
		i++
		fracStart := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		fracPart = s[fracStart:i]
		if len(fracPart) == 0 {
			return "", fmt.Errorf("invalid JSON number")
		}
	}
	expVal := 0
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		expNeg := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			if s[i] == '-' {
				expNeg = true
			}
			i++
		}
		expStart := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		expDigits := s[expStart:i]
		if len(expDigits) == 0 {
			return "", fmt.Errorf("invalid JSON number")
		}
		expDigits = strings.TrimLeft(expDigits, "0")
		if expDigits == "" {
			expDigits = "0"
		}
		if len(expDigits) > 7 {
			return "", fmt.Errorf("invalid JSON number: exponent out of range")
		}
		n := 0
		for k := 0; k < len(expDigits); k++ {
			n = n*10 + int(expDigits[k]-'0')
		}
		if expNeg {
			n = -n
		}
		if n > maxExponentAbs || n < -maxExponentAbs {
			return "", fmt.Errorf("invalid JSON number: exponent out of range")
		}
		expVal = n
	}
	if i != len(s) {
		return "", fmt.Errorf("invalid JSON number")
	}
	digits := intPart + fracPart
	decimalExp := expVal - len(fracPart)
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", nil
	}
	for len(digits) > 1 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		decimalExp++
	}
	sign := ""
	if neg {
		sign = "-"
	}
	switch {
	case decimalExp == 0:
		if len(digits) > maxCanonicalDigits {
			return "", fmt.Errorf("invalid JSON number: too many digits")
		}
		return sign + digits, nil
	case decimalExp > 0:
		if len(digits)+decimalExp > maxCanonicalDigits {
			return "", fmt.Errorf("invalid JSON number: decimal expansion too large")
		}
		return sign + digits + strings.Repeat("0", decimalExp), nil
	default:
		// decimalExp < 0: place the point or emit leading-zero fraction.
		if -decimalExp < len(digits) {
			at := len(digits) + decimalExp
			if len(digits)+1 > maxCanonicalDigits {
				return "", fmt.Errorf("invalid JSON number: too many digits")
			}
			return sign + digits[:at] + "." + digits[at:], nil
		}
		zerosNeeded := -decimalExp - len(digits)
		if zerosNeeded+len(digits)+2 > maxCanonicalDigits {
			return "", fmt.Errorf("invalid JSON number: decimal expansion too large")
		}
		return sign + "0." + strings.Repeat("0", zerosNeeded) + digits, nil
	}
}

// normalizeCanonicalValue walks a UseNumber-decoded JSON value, normalizing
// every number exactly. Objects keep Go map form (encoding/json emits keys
// sorted); arrays keep order.
func normalizeCanonicalValue(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			n, err := normalizeCanonicalValue(e)
			if err != nil {
				return nil, err
			}
			t[k] = n
		}
		return t, nil
	case []any:
		for i, e := range t {
			n, err := normalizeCanonicalValue(e)
			if err != nil {
				return nil, err
			}
			t[i] = n
		}
		return t, nil
	case json.Number:
		s, err := normalizeNumber(string(t))
		if err != nil {
			return nil, err
		}
		return json.Number(s), nil
	case string, bool, nil:
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %T (float64 must never appear)", v)
	}
}
