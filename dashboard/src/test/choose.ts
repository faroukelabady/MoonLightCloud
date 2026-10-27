import { fireEvent } from '@testing-library/svelte';

// choose picks an option in a ui/SingleSelect: open it from its trigger,
// then click the option carrying the value.
export async function choose(trigger: HTMLElement, value: string): Promise<void> {
	await fireEvent.click(trigger);
	const popup = document.getElementById(trigger.getAttribute('aria-controls') ?? '');
	const option = Array.from(popup?.querySelectorAll<HTMLElement>('[role="option"]') ?? []).find((el) => el.dataset.value === value);
	if (!option) throw new Error(`option ${value} not found`);
	await fireEvent.click(option);
}
