// Timestamps: absolute + relative, Arabic-first. Relative strings are
// computed at render time from real timestamps, never stored.

export function relativeTime(iso: string, lang: 'ar' | 'en', nowMs = Date.now()): string {
	const then = Date.parse(iso);
	if (Number.isNaN(then)) return iso;
	const diffMin = Math.max(0, Math.floor((nowMs - then) / 60000));
	if (lang === 'ar') {
		if (diffMin < 1) return 'الآن';
		if (diffMin < 60) return `منذ ${diffMin} ${diffMin === 1 ? 'دقيقة' : diffMin === 2 ? 'دقيقتين' : 'دقائق'}`;
		const h = Math.floor(diffMin / 60);
		if (h < 24) return `منذ ${h} ${h === 1 ? 'ساعة' : h === 2 ? 'ساعتين' : 'ساعات'}`;
		const d = Math.floor(h / 24);
		return `منذ ${d} ${d === 1 ? 'يوم' : d === 2 ? 'يومين' : 'أيام'}`;
	}
	if (diffMin < 1) return 'now';
	if (diffMin < 60) return `${diffMin} minute${diffMin === 1 ? '' : 's'} ago`;
	const h = Math.floor(diffMin / 60);
	if (h < 24) return `${h} hour${h === 1 ? '' : 's'} ago`;
	const d = Math.floor(h / 24);
	return `${d} day${d === 1 ? '' : 's'} ago`;
}

export function absoluteTime(iso: string, lang: 'ar' | 'en'): string {
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return iso;
	try {
		return new Intl.DateTimeFormat(lang === 'ar' ? 'ar-EG' : 'en-US', {
			dateStyle: 'medium',
			timeStyle: 'short'
		}).format(d);
	} catch {
		return iso;
	}
}

// Compact report timestamp: DD/MM/YYYY hh:mm AM/PM in the viewer's local
// time (Arabic uses ص/م), Latin digits so printed columns stay narrow.
// Same format as the Retail sales details report.
export function compactTime(iso: string, lang: 'ar' | 'en'): string {
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return iso;
	const pad = (n: number) => String(n).padStart(2, '0');
	const hours = d.getHours();
	const hour12 = hours % 12 === 0 ? 12 : hours % 12;
	const marker = lang === 'ar' ? (hours < 12 ? 'ص' : 'م') : hours < 12 ? 'AM' : 'PM';
	return `${pad(d.getDate())}/${pad(d.getMonth() + 1)}/${d.getFullYear()} ${pad(hour12)}:${pad(d.getMinutes())} ${marker}`;
}
