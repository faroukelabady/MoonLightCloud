import { describe, expect, it } from 'vitest';
import { formatMinor, formatInt, formatMicrorate, toChartNumber, subMinor, minorToDecimal, decimalToMinor } from './money.js';

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
	it('renders the audit boundary values exactly', () => {
		expect(formatMinor('0', 'EGP')).toBe('0 ج.م');
		expect(formatMinor('1', 'EGP')).toBe('0.01 ج.م');
		expect(formatMinor('99', 'EGP')).toBe('0.99 ج.م');
		expect(formatMinor('100', 'EGP')).toBe('1 ج.م');
		expect(formatMinor('9007199254740991', 'EGP')).toBe('90,071,992,547,409.91 ج.م');
		expect(formatMinor('9007199254740992', 'EGP')).toBe('90,071,992,547,409.92 ج.م');
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

describe('subMinor', () => {
	it('subtracts exact minor units (net never clamps)', () => {
		expect(subMinor('100000', '25000')).toBe('75000');
		expect(subMinor('0', '0')).toBe('0');
	});
	it('yields negative net for refund-heavy periods', () => {
		expect(subMinor('1000', '2500')).toBe('-1500');
		expect(formatMinor(subMinor('1000', '2500'), 'EGP')).toBe('-15 ج.م');
	});
	it('stays exact beyond float precision', () => {
		expect(subMinor('9007199254740993', '1')).toBe('9007199254740992');
	});
});

describe('formatMicrorate', () => {
	it('renders exact rates with at least two decimals', () => {
		expect(formatMicrorate('52000000')).toBe('52.00');
		expect(formatMicrorate('52500000')).toBe('52.50');
		expect(formatMicrorate('49875500')).toBe('49.8755');
		expect(formatMicrorate('1234000001')).toBe('1,234.000001');
	});
});

describe('price fields in pounds', () => {
	it('shows minor units as a plain two-decimal amount', () => {
		expect(minorToDecimal('56000')).toBe('560.00');
		expect(minorToDecimal('5')).toBe('0.05');
		expect(minorToDecimal('0')).toBe('0.00');
		expect(minorToDecimal(null)).toBe('0.00');
		expect(minorToDecimal('9007199254740993')).toBe('90071992547409.93');
	});
	it('reads pounds back into exact minor units', () => {
		expect(decimalToMinor('560')).toBe('56000');
		expect(decimalToMinor('560.5')).toBe('56050');
		expect(decimalToMinor('560.50')).toBe('56050');
		expect(decimalToMinor(' 1,200.05 ')).toBe('120005');
		expect(decimalToMinor('٥٦٠٫٢٥')).toBe('56025');
		expect(decimalToMinor('0')).toBe('0');
		expect(decimalToMinor('90071992547409.93')).toBe('9007199254740993');
	});
	it('rejects anything that is not a plain non-negative amount', () => {
		for (const bad of ['', '.', '-5', '1.234', '12a', '1e3', '١.٢.٣']) {
			expect(decimalToMinor(bad)).toBeNull();
		}
	});
});
