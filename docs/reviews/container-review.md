# Container/Deployment Review (read-only)

Reviewers do not modify production code. One line per finding: `path:line: severity: problem. fix.`

Severities: Critical / High / Medium / Low. Critical/High block Phase 1A.

## Checklist

- [ ] Multi-stage build, `-trimpath`, static binary, minimal runtime?
- [ ] Runs non-root, no toolchain/source/secrets in final image?
- [ ] Stateless (no authoritative local disk); graceful SIGTERM?
- [ ] Compose healthchecks real (pg_isready, /health/ready), no `sleep 10`?
- [ ] Portable (Podman/Docker/Railway/Render/VPS) with no host-specific code?
- [ ] CI pins Actions by SHA, builds OCI, runs real-Postgres integration?
