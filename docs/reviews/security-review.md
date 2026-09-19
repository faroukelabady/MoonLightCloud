# Security Review (read-only)

Reviewers do not modify production code. One line per finding: `path:line: severity: problem. fix.`

Severities: Critical / High / Medium / Low. Critical/High block Phase 1A.

## Checklist

- [ ] Threat-model row affected? Current vs future mitigations separated?
- [ ] AuthN/Z on new endpoints; enumeration-safe failures?
- [ ] Secrets: random generation, hash at rest, shown once, redacted logs?
- [ ] Body/header/timeout posture kept? CORS still closed?
