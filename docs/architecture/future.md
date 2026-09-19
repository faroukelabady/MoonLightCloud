# Future Target Architecture (not implemented in Phase 1A)

```
                     Commerce Platform
                Shopify / WooCommerce
                         │
                         ▼
                  MoonLightCloud
              ┌───────────────────┐
              │ Go                │
              │ PostgreSQL        │
              │ Sync              │
              │ Analytics         │
              │ Notifications     │
              └─────────▲─────────┘
                        │
                        ▼
                 MoonLightRetail
                    SQLite
```

Stated explicitly:

- Commerce provider is replaceable (ADR-0007).
- Notification provider is replaceable (ADR-0008).
- Hosting provider is replaceable (ADR-0010).
- PostgreSQL provider is replaceable (ADR-0003).
- The MoonLightCloud API contract belongs to MoonLight Software (ADR-0006).

Nothing above beyond devices/auth/health/version exists in Phase 1A.
Roadmap phases add sync ingestion, commerce, notifications, dashboard, and
scheduled reporting — each behind the boundaries established here.
