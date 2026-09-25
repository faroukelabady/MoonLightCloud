// Exact money handling: minor-unit strings stay strings end to end.
// No parseFloat, no float arithmetic. Formatting uses BigInt only, so
// values beyond Number.MAX_SAFE_INTEGER still render exactly. Only chart
// coordinates convert to Number, via toChartNumber with a loud safe-range
// check (unrealistic magnitudes become an error state, never silent loss).

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
	return `${negative ? '-' : ''}${grouped}${fracPart} ج.م`;
}

function groupDigits(s: string): string {
	return s.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
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
