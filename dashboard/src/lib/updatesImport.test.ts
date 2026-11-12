import { describe, it, expect } from 'vitest';
import { readReleaseImportFile } from './updatesApi';

describe('release import file', () => {
	const envelope = { format: 'moonlight-release-envelope-v2', manifest: btoa(JSON.stringify({ version: '1.8.0', release_sequence: 23, artifacts: [{ file_name: 'setup.exe' }] })), sigstore_bundle: {} };
	it('fills the existing form from the CI public import file', () => {
		const result = readReleaseImportFile(JSON.stringify({ envelope, artifact_urls: { 'setup.exe': 'https://releases.test/setup.exe', unrelated: 'https://other.test/a' } }));
		expect(JSON.parse(result.envelope)).toEqual(envelope);
		expect(result.urls).toEqual({ 'setup.exe': 'https://releases.test/setup.exe' });
	});
	it('keeps standalone and malformed envelope input for existing validation', () => {
		for (const text of [JSON.stringify(envelope), '{', 'null']) expect(readReleaseImportFile(text)).toEqual({ envelope: text, urls: {} });
	});
});
