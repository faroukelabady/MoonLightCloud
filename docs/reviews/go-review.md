# Go Review (read-only)

Reviewers do not modify production code. One line per finding: `path:line: severity: problem. fix.`

Severities: Critical / High / Medium / Low. Critical/High block Phase 1A.

## Checklist

- [ ] gofmt clean, go vet clean, race-clean?
- [ ] Errors classified via apperr; no driver errors to clients?
- [ ] Contexts with deadlines on I/O; graceful shutdown preserved?
- [ ] No secret material in logs/errors?
