# Architecture Review (read-only)

Reviewers do not modify production code. One line per finding: `path:line: severity: problem. fix.`

Severities: Critical / High / Medium / Low. Critical/High block Phase 1A.

## Checklist

- [ ] Modular monolith intact? No new infra (Redis/broker/K8s) without ADR?
- [ ] Domain packages free of pgx/net/http imports?
- [ ] New code behind the right boundary (auth/sync/commerce/notifications/adapters)?
- [ ] No Railway/vendor-specific coupling?
