# Sync Review (read-only)

Reviewers do not modify production code. One line per finding: `path:line: severity: problem. fix.`

Severities: Critical / High / Medium / Low. Critical/High block Phase 1A.

## Checklist

- [ ] Contract versioned (/api/v1 or event_version)? Backward compatible?
- [ ] Idempotency keys + server dedup for mutations?
- [ ] No shared domain package introduced?
- [ ] At-least-once assumption honored (safe retries)?
