import { describe, expect, it } from 'vitest';
import { calculateZakat, parseMajor } from './zakat';

describe('calculateZakat', () => {
	it('adds cash and receivables, subtracts debts and applies 2.5%', () => {
		const r = calculateZakat({ inventory: 10_000_000n, cash: 2_000_000n, receivables: 500_000n, debts: 1_500_000n, goldGram: 400_000n, year: 'hijri' });
		expect(r.base).toBe(11_000_000n);
		expect(r.nisab).toBe(34_000_000n);
		expect(r.reachesNisab).toBe(false);
		expect(r.zakat).toBe(0n);
	});

	it('charges zakat once the base reaches nisab, with the Gregorian rate', () => {
		const r = calculateZakat({ inventory: 100_000_000n, cash: 0n, receivables: 0n, debts: 0n, goldGram: 400_000n, year: 'gregorian' });
		expect(r.reachesNisab).toBe(true);
		expect(r.zakat).toBe(2_577_000n);
	});

	it('estimates without a gold price and never goes below zero', () => {
		expect(calculateZakat({ inventory: 1_000n, cash: 0n, receivables: 0n, debts: 0n, goldGram: null, year: 'hijri' })).toEqual({ base: 1_000n, nisab: null, reachesNisab: null, zakat: 25n });
		expect(calculateZakat({ inventory: 0n, cash: 0n, receivables: 0n, debts: 5n, goldGram: null, year: 'hijri' }).base).toBe(0n);
	});
});

describe('parseMajor', () => {
	it('reads EGP amounts into minor units', () => {
		expect(parseMajor('1,250.5')).toBe(125_050n);
		expect(parseMajor('١٢٥٠')).toBe(125_000n);
		expect(parseMajor('  ')).toBeNull();
		expect(parseMajor('-3')).toBeUndefined();
		expect(parseMajor('1.234')).toBeUndefined();
	});
});
