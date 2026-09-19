# Database Review (read-only)

Reviewers do not modify production code. One line per finding: `path:line: severity: problem. fix.`

Severities: Critical / High / Medium / Low. Critical/High block Phase 1A.

## Checklist

- [ ] Migration is append-only, small, reversible (up/down)?
- [ ] UUID domain identity, TIMESTAMPTZ UTC?
- [ ] Queries parameterized via sqlc; generated code untouched?
- [ ] Indexes for new access paths? No SELECT * in hot paths?
- [ ] Pool/timeout impact considered?
