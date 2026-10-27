import { afterEach, describe, expect, it, vi } from 'vitest';
import { downloadRecoveryCodes, recoveryCodesFilename, recoveryCodesText } from './recoveryCodes';

afterEach(() => vi.restoreAllMocks());

describe('recovery codes file', () => {
	it('lists every code with the account and creation time', () => {
		const text = recoveryCodesText('owner@shop.test', ['AAAA-BBBB', 'CCCC-DDDD'], new Date('2026-10-27T10:00:00Z'));
		expect(text).toContain('Account: owner@shop.test');
		expect(text).toContain('Created: 2026-10-27T10:00:00.000Z');
		expect(text).toContain('1. AAAA-BBBB\n2. CCCC-DDDD');
	});

	it('names the file after a filesystem-safe login', () => {
		expect(recoveryCodesFilename('Owner@Shop.test')).toBe('moonlightcloud-recovery-codes-owner-shop.test.txt');
		expect(recoveryCodesFilename('')).toBe('moonlightcloud-recovery-codes.txt');
	});

	it('saves the codes through a temporary local link', async () => {
		let blob: Blob | null = null;
		Object.assign(URL, { createObjectURL: vi.fn((b: Blob) => ((blob = b), 'blob:codes')), revokeObjectURL: vi.fn() });
		const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
			expect(this.download).toBe('moonlightcloud-recovery-codes-owner.txt');
			expect(this.href).toBe('blob:codes');
		});
		downloadRecoveryCodes('owner', ['AAAA-BBBB']);
		expect(click).toHaveBeenCalledOnce();
		expect(await (blob as unknown as Blob).text()).toContain('1. AAAA-BBBB');
		expect(document.querySelector('a[download]')).toBeNull();
	});
});
