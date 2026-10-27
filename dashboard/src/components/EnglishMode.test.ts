import { afterEach, describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import Sidebar from './Sidebar.svelte';
import WidgetError from './WidgetError.svelte';
import BranchCard from './BranchCard.svelte';
import LoginPage from './LoginPage.svelte';
import { formatMinor } from '../lib/money.js';
import { lang, setLang } from '../lib/i18n.svelte.js';

afterEach(() => setLang('ar'));

describe('English mode', () => {
	it('switches the document to LTR English and remembers the choice', () => {
		setLang('en');
		expect(lang()).toBe('en');
		expect(document.documentElement.lang).toBe('en');
		expect(document.documentElement.dir).toBe('ltr');
		expect(localStorage.getItem('moonlight.dashboard.lang')).toBe('en');
		setLang('ar');
		expect(document.documentElement.dir).toBe('rtl');
	});

	it('shows English labels without the Arabic pairing', () => {
		setLang('en');
		render(Sidebar, { props: { route: 'overview', navigate: () => {} } });
		expect(screen.getByRole('navigation', { name: 'Main navigation' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Overview' })).toBeTruthy();
		expect(document.body.textContent).not.toMatch(/[؀-ۿ]/);
	});

	it('localizes errors, branch names and money', () => {
		setLang('en');
		render(WidgetError, { props: { status: 503, onretry: () => {} } });
		expect(screen.getByText('Reporting is temporarily unavailable')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();
		const rows = [{ shop_name_ar: 'الأقصر', shop_name_en: 'Luxor', shop_phone: '0', channel: 'STORE', currency: 'EGP', transactions: 1, units: 1, return_transactions: 0, units_returned: 0, subtotal_minor: '100', sales_total_minor: '100', refund_total_minor: '0', returned_cost_minor: '0' }];
		render(BranchCard, { props: { rows, status: 'loaded', errStatus: null, onretry: () => {} } });
		expect(screen.getByText('Luxor')).toBeTruthy();
		expect(formatMinor('12458000', 'EGP')).toBe('124,580 EGP');
	});

	it('offers the language switch before sign-in', async () => {
		render(LoginPage, { props: { onauthenticated: () => {} } });
		const toggle = screen.getByRole('button', { name: 'English' });
		toggle.click();
		expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'العربية' })).toBeTruthy();
	});
});
