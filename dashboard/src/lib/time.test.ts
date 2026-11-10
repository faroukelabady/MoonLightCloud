import { describe, expect, it } from 'vitest';
import { compactTime } from './time';

describe('compactTime', () => {
	it('formats DD/MM/YYYY hh:mm AM/PM in local time', () => {
		const at = (h: number, m: number) => new Date(2026, 10, 3, h, m).toISOString();
		expect(compactTime(at(0, 5), 'en')).toBe('03/11/2026 12:05 AM');
		expect(compactTime(at(9, 7), 'en')).toBe('03/11/2026 09:07 AM');
		expect(compactTime(at(12, 0), 'en')).toBe('03/11/2026 12:00 PM');
		expect(compactTime(at(23, 59), 'en')).toBe('03/11/2026 11:59 PM');
		expect(compactTime(at(14, 30), 'ar')).toBe('03/11/2026 02:30 م');
		expect(compactTime('not-a-date', 'en')).toBe('not-a-date');
	});
});
