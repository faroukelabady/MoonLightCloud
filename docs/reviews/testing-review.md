# Testing Review (read-only)

Reviewers do not modify production code. One line per finding: `path:line: severity: problem. fix.`

Severities: Critical / High / Medium / Low. Critical/High block Phase 1A.

## Checklist

- [ ] Unit + integration (real PG) coverage for new behavior?
- [ ] Isolated test DBs; no dev/prod targeting; parallel-safe?
- [ ] Race + vet + govulncheck green?
