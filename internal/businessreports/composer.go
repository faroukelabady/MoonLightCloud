package businessreports

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// ReportSource is the frozen canonical reporting boundary the
// composer reads through. No financial SQL lives here.
type ReportSource interface {
	ParseRequest(kind, fromDate, toDate, currency string) (report.Request, error)
	Summary(ctx context.Context, req report.Request) (report.Summary, error)
}

// ComposeReport resolves the canonical summary for one civil period
// and renders the deterministic localized body plus its fingerprint.
// Identical (kind, period, summary, locale) always yields identical
// bytes: fixed line order, fixed bucket order, no timestamps, no IDs.
func ComposeReport(ctx context.Context, source ReportSource, kind ReportKind, from, to, locale string) (body string, fingerprint [32]byte, err error) {
	if err := ValidateLocale(locale); err != nil {
		return "", [32]byte{}, err
	}
	request, err := source.ParseRequest(report.PeriodCustom, from, to, "")
	if err != nil {
		return "", [32]byte{}, err
	}
	summary, err := source.Summary(ctx, request)
	if err != nil {
		return "", [32]byte{}, apperr.Wrap(apperr.Internal, "report composition", err)
	}
	body, err = renderBody(kind, from, to, locale, summary)
	if err != nil {
		return "", [32]byte{}, err
	}
	if len(body) > notifications.MaxParameterValueLen {
		return "", [32]byte{}, apperr.New(apperr.InvalidInput, CodeBodyTooLarge)
	}
	sum := sha256.New()
	for _, field := range []string{string(kind), from, to, locale, body} {
		sum.Write([]byte(field))
		sum.Write([]byte{0})
	}
	var digest [32]byte
	copy(digest[:], sum.Sum(nil))
	return body, digest, nil
}

// renderBody emits the fixed v1 aggregate summary: period, counts,
// per-currency gross/refunds/net/cost, optional freshness. Only
// canonical summary fields appear; no new financial definitions.
func renderBody(kind ReportKind, from, to, locale string, summary report.Summary) (string, error) {
	var lines []string
	if locale == LocaleAR {
		if kind == ReportTenDay {
			lines = append(lines, "تقرير المبيعات (10 أيام)")
		} else {
			lines = append(lines, "تقرير المبيعات اليومي")
		}
		lines = append(lines, "الفترة: "+periodLabel(from, to))
		lines = append(lines, fmt.Sprintf("المعاملات: %d", summary.TransactionCount))
	} else {
		if kind == ReportTenDay {
			lines = append(lines, "10-Day Business Report")
		} else {
			lines = append(lines, "Daily Business Report")
		}
		lines = append(lines, "Period: "+periodLabel(from, to))
		lines = append(lines, fmt.Sprintf("Transactions: %d", summary.TransactionCount))
	}
	buckets := append([]report.CurrencyTotal(nil), summary.CurrencyTotals...)
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Currency < buckets[j].Currency })
	for _, section := range []struct {
		ar, en string
		pick   func(report.CurrencyTotal) int64
	}{
		{ar: "إجمالي المبيعات:", en: "Gross sales:", pick: func(c report.CurrencyTotal) int64 { return c.SalesTotalMinor }},
		{ar: "المرتجعات:", en: "Refunds:", pick: func(c report.CurrencyTotal) int64 { return c.RefundTotalMinor }},
		{ar: "صافي المبيعات:", en: "Net sales:", pick: func(c report.CurrencyTotal) int64 { return c.NetSalesMinor }},
		{ar: "التكلفة:", en: "Cost:", pick: func(c report.CurrencyTotal) int64 { return c.NetCostMinor }},
	} {
		if locale == LocaleAR {
			lines = append(lines, section.ar)
		} else {
			lines = append(lines, section.en)
		}
		for _, bucket := range buckets {
			lines = append(lines, bucket.Currency+" "+formatMinor(section.pick(bucket)))
		}
	}
	if at := summary.Freshness.LatestProjectedSaleOccurredAt; at != nil {
		if locale == LocaleAR {
			lines = append(lines, "بيانات السحابة حتى: "+at.UTC().Format("2006-01-02T15:04:05Z"))
		} else {
			lines = append(lines, "Cloud data through: "+at.UTC().Format("2006-01-02T15:04:05Z"))
		}
	}
	return strings.Join(lines, "\n"), nil
}

// periodLabel renders a single date or an inclusive range identically
// in both locales (ISO dates are locale-neutral).
func periodLabel(from, to string) string {
	if from == to {
		return from
	}
	return from + " - " + to
}

// formatMinor renders exact minor units as major.frac with two
// fraction digits (EGP piastres, USD cents). No floats, no grouping:
// byte-deterministic.
func formatMinor(minor int64) string {
	negative := minor < 0
	value := minor
	if negative {
		value = -value
	}
	major := value / 100
	frac := value % 100
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, major, frac)
}
