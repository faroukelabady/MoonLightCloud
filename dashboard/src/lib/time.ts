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
