// Test-only local HTTPS terminator for the dashboard E2E runner
// (scripts/e2e-dashboard.sh). Production deployments terminate TLS in their
// own reverse proxy; this exists so Playwright exercises the production
// __Host-/Secure session cookie over real HTTPS on 127.0.0.1.
//
// Usage: node tls-proxy.mjs <listen-port> <upstream-http-origin> <cert.pem> <key.pem>
// Loopback only. The Host header is forwarded unchanged so Cloud's
// same-origin (Origin/Referer vs Host) check sees the browser's origin.
import { createServer } from 'node:https';
import { request } from 'node:http';
import { readFileSync } from 'node:fs';

const [port, upstream, certFile, keyFile] = process.argv.slice(2);
if (!port || !upstream || !certFile || !keyFile) {
	console.error('usage: tls-proxy.mjs <listen-port> <upstream-http-origin> <cert.pem> <key.pem>');
	process.exit(2);
}
const target = new URL(upstream);

const server = createServer({ cert: readFileSync(certFile), key: readFileSync(keyFile) }, (req, res) => {
	const up = request(
		{ host: target.hostname, port: target.port, method: req.method, path: req.url, headers: req.headers },
		(upRes) => {
			res.writeHead(upRes.statusCode ?? 502, upRes.headers);
			upRes.pipe(res);
		}
	);
	up.on('error', () => {
		if (!res.headersSent) res.writeHead(502);
		res.end();
	});
	req.pipe(up);
});
server.listen(Number(port), '127.0.0.1', () => console.log(`tls-proxy: https://127.0.0.1:${port} -> ${upstream}`));
