import { expect, type Locator } from '@playwright/test';

// The dashboard selectors are ui/SingleSelect listboxes, not native
// <select>s: open the trigger and click the option carrying the value.
export async function choose(trigger: Locator, value: string): Promise<void> {
	await trigger.click();
	const popup = trigger.page().locator(`#${await trigger.getAttribute('aria-controls')}`);
	await popup.locator(`[role="option"][data-value="${value}"]`).click();
	await expect(trigger).toHaveAttribute('data-value', value);
}

// optionCount opens a selector, counts its options and closes it again.
export async function optionCount(trigger: Locator): Promise<number> {
	await trigger.click();
	const popup = trigger.page().locator(`#${await trigger.getAttribute('aria-controls')}`);
	const count = await popup.locator('[role="option"]').count();
	await trigger.click();
	return count;
}
