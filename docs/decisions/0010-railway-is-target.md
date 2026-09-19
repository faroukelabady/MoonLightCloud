# ADR-0010: Railway is a deployment target, not a dependency

## Status

Accepted.

## Context

Hosting must stay replaceable (Render/Coolify/Cloud Run/AWS/VPS). Railway SDKs/APIs in code would lock us in.

## Decision

App depends only on HTTP + PostgreSQL + env + OCI. Needs PORT, DATABASE_URL, /health/ready, stdout logs, SIGTERM. No Railway code; assumptions in docs/operations/railway.md.

## Consequences

Documented in the linked guides; revisited only by a new ADR.
