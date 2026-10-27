// Dashboard language. Arabic keeps the approved bilingual presentation
// (Arabic primary, English sublabel); English shows English only, LTR.
// The choice is a per-browser preference and never reaches the server.
export type Lang = 'ar' | 'en';

const storageKey = 'moonlight.dashboard.lang';

function initialLang(): Lang {
	try {
		return localStorage.getItem(storageKey) === 'en' ? 'en' : 'ar';
	} catch {
		return 'ar';
	}
}

const state = $state<{ lang: Lang }>({ lang: initialLang() });

function applyDocument(lang: Lang) {
	if (typeof document === 'undefined') return;
	document.documentElement.lang = lang;
	document.documentElement.dir = lang === 'ar' ? 'rtl' : 'ltr';
}
applyDocument(state.lang);

export function lang(): Lang {
	return state.lang;
}

export function setLang(next: Lang) {
	state.lang = next;
	applyDocument(next);
	try {
		localStorage.setItem(storageKey, next);
	} catch {
		// Private windows may refuse storage; the choice lasts this page only.
	}
}

// t picks one language: Arabic in Arabic mode, English otherwise.
export function t(ar: string, en: string): string {
	return state.lang === 'ar' ? ar : en;
}

// tb keeps the Arabic-mode "عربي / English" pairing and shows English alone
// in English mode.
export function tb(ar: string, en: string): string {
	return state.lang === 'ar' ? `${ar} / ${en}` : en;
}

// sub is the muted sublabel under a primary label: English in Arabic mode,
// nothing in English mode.
export function sub(en: string | undefined): string {
	return state.lang === 'ar' ? (en ?? '') : '';
}

// arabic is true in Arabic mode, where English sublabels are shown.
export function arabic(): boolean {
	return state.lang === 'ar';
}
