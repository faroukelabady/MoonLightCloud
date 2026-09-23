import { describe, expect, it } from 'vitest';
import { formatMinor, formatInt, toChartNumber } from './money.js';

describe('formatMinor', () => {
	it('formats EGP without decimals when exact', () => {
		expect(formatMinor('12458000', 'EGP')).toBe('124,580 ج.م');
	});
	it('formats EGP piasters exactly', () => {
		expect(formatMinor('12458033', 'EGP')).toBe('124,580.33 ج.م');
	});
	it('formats USD cents exactly', () => {
		expect(formatMinor('254033', 'USD')).toBe('$2,540.33');
	});
	it('handles zero and negatives without float', () => {
		expect(formatMinor('0', 'EGP')).toBe('0 ج.م');
		expect(formatMinor('-150', 'USD')).toBe('-$1.50');
	});
	it('keeps huge int64 values exact (beyond float precision)', () => {
		// 2^53 + 1 would round under Number(); BigInt keeps every digit.
		expect(formatMinor('9007199254740993', 'EGP')).toBe('90,071,992,547,409.93 ج.م');
	});
	it('never uses parseFloat paths', () => {
		expect(formatMinor('0000123', 'EGP')).toBe('1.23 ج.م');
	});
});

describe('formatInt', () => {
	it('groups thousands', () => {
		expect(formatInt(1234567)).toBe('1,234,567');
	});
});

describe('toChartNumber', () => {
	it('converts minor units to major for coordinates', () => {
		expect(toChartNumber('254033')).toBeCloseTo(2540.33);
	});
	it('refuses unsafe integers loudly instead of rounding', () => {
		expect(() => toChartNumber('9007199254740993')).toThrow();
	});
});
