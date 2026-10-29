// Dashboard language. Interface text lives in src/locales/<lang>.json and
// is looked up by key with msg(); adding a language is one more file plus
// its Lang entry. Arabic mode keeps the approved bilingual presentation
// where a screen asks for it (msgb: "عربي / English"); English shows English
// only, LTR. The choice is a per-browser preference and never reaches the
// server.
import ar from '../locales/ar.json';
import en from '../locales/en.json';

export type Lang = 'ar' | 'en';

type Messages = { [key: string]: string | Messages };
const catalogs: Record<Lang, Messages> = { ar, en };

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

type Params = Record<string, string | number>;

function lookup(lang: Lang, key: string): string | undefined {
	let node: string | Messages | undefined = catalogs[lang];
	for (const part of key.split('.')) {
		if (typeof node !== 'object') return undefined;
		node = node[part];
	}
	return typeof node === 'string' ? node : undefined;
}

function fill(text: string, params?: Params): string {
	return params ? text.replace(/\{(\w+)\}/g, (whole, name: string) => (name in params ? String(params[name]) : whole)) : text;
}

// messageIn returns one language's text for key ({name} placeholders
// filled), falling back to Arabic and then to the key itself so a missing
// entry is visible instead of blank.
export function messageIn(lang: Lang, key: string, params?: Params): string {
	return fill(lookup(lang, key) ?? lookup('ar', key) ?? key, params);
}

// msg is the current language's text for key.
export function msg(key: string, params?: Params): string {
	return messageIn(state.lang, key, params);
}

// msgb is the bilingual form: "عربي / English" in Arabic mode, English
// alone in English mode.
export function msgb(key: string, params?: Params): string {
	return state.lang === 'ar' ? `${messageIn('ar', key, params)} / ${messageIn('en', key, params)}` : messageIn('en', key, params);
}
