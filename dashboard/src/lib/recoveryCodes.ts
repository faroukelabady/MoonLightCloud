// Recovery codes are shown once; this lets the operator keep them as a text
// file. The file is built in the browser and never sent anywhere.

export function recoveryCodesText(login: string, codes: string[], createdAt = new Date()): string {
	return [
		'MoonLightCloud recovery codes',
		`Account: ${login}`,
		`Created: ${createdAt.toISOString()}`,
		'',
		'Each code signs in once if you lose your authenticator app.',
		'Keep this file somewhere safe and offline; anyone with a code and your password can sign in.',
		'',
		...codes.map((code, index) => `${index + 1}. ${code}`),
		''
	].join('\n');
}

export function recoveryCodesFilename(login: string): string {
	const safe = login.toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '');
	return `moonlightcloud-recovery-codes${safe ? `-${safe}` : ''}.txt`;
}

export function downloadRecoveryCodes(login: string, codes: string[]): void {
	const url = URL.createObjectURL(new Blob([recoveryCodesText(login, codes)], { type: 'text/plain;charset=utf-8' }));
	const link = document.createElement('a');
	link.href = url;
	link.download = recoveryCodesFilename(login);
	document.body.append(link);
	link.click();
	link.remove();
	setTimeout(() => URL.revokeObjectURL(url), 0);
}
