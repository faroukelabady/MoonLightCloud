import { afterEach, describe, expect, it } from 'vitest';
import ar from '../locales/ar.json';
import en from '../locales/en.json';
import { messageIn, msg, setLang } from './i18n.svelte.js';

function flattenKeys(value: Record<string, unknown>, prefix = ''): string[] {
	return Object.entries(value).flatMap(([key, nested]) => {
		const path = prefix ? `${prefix}.${key}` : key;
		return nested && typeof nested === 'object' ? flattenKeys(nested as Record<string, unknown>, path) : [path];
	});
}

afterEach(() => setLang('ar'));

describe('dashboard locale files', () => {
	it('keep identical key sets in ar and en', () => {
		expect(flattenKeys(ar).sort()).toEqual(flattenKeys(en).sort());
	});

	it('use the same placeholders in both languages', () => {
		for (const key of flattenKeys(en)) {
			const names = (lang: 'ar' | 'en') => (messageIn(lang, key).match(/\{\w+\}/g) ?? []).sort();
			expect(names('ar'), key).toEqual(names('en'));
		}
	});

	it('have no empty text', () => {
		for (const key of flattenKeys(en)) {
			expect(messageIn('ar', key).trim(), key).not.toBe('');
			expect(messageIn('en', key).trim(), key).not.toBe('');
		}
	});
});

describe('msg', () => {
	it('follows the chosen language and fills placeholders', () => {
		setLang('en');
		expect(msg('catalogAdmin.variantMany', { count: 3 })).toBe('3 variants');
		setLang('ar');
		expect(msg('catalogAdmin.variantMany', { count: 3 })).toBe('3 خيارات');
	});


	it('shows the key for a missing entry instead of blank text', () => {
		expect(msg('nowhere.missing')).toBe('nowhere.missing');
	});
});
