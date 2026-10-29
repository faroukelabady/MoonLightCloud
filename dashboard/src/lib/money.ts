// Exact money handling: minor-unit strings stay strings end to end.
// No parseFloat, no float arithmetic. Formatting uses BigInt only, so
// values beyond Number.MAX_SAFE_INTEGER still render exactly. Only chart
// coordinates convert to Number, via toChartNumber with a loud safe-range
// check (unrealistic magnitudes become an error state, never silent loss).
import { t } from './i18n.svelte.js';

export function formatMinor(minor: string, currency: 'EGP' | 'USD'): string {
	const negative = minor.startsWith('-');
	const digits = (negative ? minor.slice(1) : minor).replace(/[^0-9]/g, '') || '0';
	const v = BigInt(digits || '0');
	const major = v / 100n;
	const frac = v % 100n;
	const grouped = groupDigits(major.toString());
	if (currency === 'USD') {
		return `${negative ? '-' : ''}$${grouped}.${frac.toString().padStart(2, '0')}`;
	}
	const fracPart = frac === 0n ? '' : '.' + frac.toString().padStart(2, '0');
	return `${negative ? '-' : ''}${grouped}${fracPart} ${t('ج.م', 'EGP')}`;
}

function groupDigits(s: string): string {
	return s.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

// formatMicrorate renders a rate x 1,000,000 exactly ("52.5" -> "52.50"):
// at least two decimals, trailing zeros dropped beyond them.
export function formatMicrorate(microrate: string): string {
	const v = BigInt(microrate.replace(/[^0-9]/g, '') || '0');
	const frac = (v % 1_000_000n).toString().padStart(6, '0').replace(/0+$/, '').padEnd(2, '0');
	return `${groupDigits((v / 1_000_000n).toString())}.${frac}`;
}

export function formatInt(n: number | string): string {
	return groupDigits(String(n));
}

// subMinor subtracts exact minor-unit strings with BigInt (net may be
// negative; BigInt never overflows, so no silent wrap is possible).
export function subMinor(gross: string, refund: string): string {
	return (BigInt(gross || '0') - BigInt(refund || '0')).toString();
}

// toChartNumber converts an exact minor-unit string for chart coordinates.
// Throws (→ error state) instead of silently losing precision.
export function toChartNumber(minor: string): number {
	const n = Number(minor);
	if (!Number.isSafeInteger(n)) {
		throw new Error('value exceeds safe chart range');
	}
	return n / 100;
}

// minorToDecimal shows an exact minor-unit string as a plain decimal for an
// input field ("56000" -> "560.00"): no grouping and no currency sign.
export function minorToDecimal(minor: string | null | undefined): string {
	const negative = (minor ?? '').startsWith('-');
	const v = BigInt((minor ?? '').replace(/[^0-9]/g, '') || '0');
	return `${negative && v !== 0n ? '-' : ''}${v / 100n}.${(v % 100n).toString().padStart(2, '0')}`;
}

// decimalToMinor parses a non-negative amount typed in pounds or dollars
// ("560", "560.5", "1,200.50", Arabic-Indic digits) into exact minor units
// ("56000"). It returns null for anything else, including a third decimal.
export function decimalToMinor(text: string): string | null {
	const ascii = text
		.trim()
		.replace(/[٠-٩]/g, (d) => String(d.charCodeAt(0) - 0x0660))
		.replace(/[۰-۹]/g, (d) => String(d.charCodeAt(0) - 0x06f0))
		.replace(/٫/g, '.')
		.replace(/[,٬\s]/g, '');
	const m = /^(\d+)(?:\.(\d{0,2}))?$/.exec(ascii);
	if (!m) return null;
	return (BigInt(m[1]) * 100n + BigInt((m[2] ?? '').padEnd(2, '0') || '0')).toString();
}
