// Zakat on trade goods (عروض التجارة) in EGP minor units, the same rules
// as MoonLightRetail's stocktaking screen. BigInt only, so no amount is
// ever rounded by floating point. The page keeps the inputs in memory.

// Nisab is the value of 85 grams of gold.
export const NISAB_GOLD_GRAMS = 85n;

// 2.5% for a lunar (Hijri) year; 2.577% when the shop closes its zakat
// year on the longer Gregorian calendar.
export type ZakatYear = 'hijri' | 'gregorian';
const RATES: Record<ZakatYear, { numerator: bigint; denominator: bigint }> = {
	hijri: { numerator: 25n, denominator: 1000n },
	gregorian: { numerator: 2577n, denominator: 100000n }
};

export interface ZakatInput {
	inventory: bigint;
	cash: bigint;
	receivables: bigint;
	debts: bigint;
	goldGram: bigint | null;
	year: ZakatYear;
}

export interface ZakatResult {
	// Inventory + cash + expected receivables − debts due, never negative.
	base: bigint;
	nisab: bigint | null;
	// null until a gold price is entered.
	reachesNisab: boolean | null;
	// Zakat on the base; 0 when the base is below nisab.
	zakat: bigint;
}

export function calculateZakat(input: ZakatInput): ZakatResult {
	const total = input.inventory + input.cash + input.receivables - input.debts;
	const base = total > 0n ? total : 0n;
	const nisab = input.goldGram == null ? null : input.goldGram * NISAB_GOLD_GRAMS;
	const reachesNisab = nisab == null ? null : base >= nisab;
	const rate = RATES[input.year];
	// Half up: base is never negative.
	const zakat = reachesNisab === false ? 0n : (base * rate.numerator * 2n + rate.denominator) / (rate.denominator * 2n);
	return { base, nisab, reachesNisab, zakat };
}

// parseMajor reads a typed EGP amount ("1250", "1,250.5", "١٢٥٠") into
// minor units. Empty is null; anything else that is not a non-negative
// amount with at most two decimals is undefined.
export function parseMajor(raw: string): bigint | null | undefined {
	const latin = raw.trim().replace(/[٠-٩]/g, (d) => String(d.charCodeAt(0) - 0x660)).replace(/[٫]/g, '.').replace(/[,٬\s]/g, '');
	if (latin === '') return null;
	const match = /^(\d+)(?:\.(\d{1,2}))?$/.exec(latin);
	if (!match) return undefined;
	return BigInt(match[1]) * 100n + BigInt((match[2] ?? '').padEnd(2, '0'));
}
