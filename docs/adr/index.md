---
id: index
title: Architecture decision records
sidebar_label: ADR index
slug: /
sidebar_position: 0
---

# Architecture decision records

The decisions of `customer-payments`, in the order they were taken. The ADR bodies are
immutable; a changed decision gets a new ADR that supersedes the old one. The
*Status* column is copied from each ADR's own status line.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](/docs/adr/0001-customer-payments-bounded-context) | Customer Payments is a bounded context of the storefront domain | Accepted (bootstrap). Design source: warehouse-docs `storefront/model/model.yaml`, context `customer-payments`. |
| [0002](/docs/adr/0002-customer-payments-assumptions) | customer-payments assumptions | Accepted (2026-10-25). Where the frozen model and `model-gap-decisions.md` (H1-H12, S4) differ, the decisions win; everything else the model leaves open is recorded in the ADR. |
| [0003](/docs/adr/0003-cloudevents-envelope-and-type-catalogue) | CloudEvents envelope and type catalogue | Accepted (2026-10-25). |
| [0004](/docs/adr/0004-fleet-observability-standard) | Adopt the fleet observability standard (logs, metrics, traces) | Accepted (2026-10-26). |
| [0005](/docs/adr/0005-a2a-inbound-adapter) | A2A inbound adapter | Accepted (2026-10-30). Follows ADR 0003 (CloudEvents on every Kafka message) and ADR 0004 (the shared telemetry contract); modelled on the read-only MCP adapter of this context. |
| [0006](/docs/adr/0006-adopt-the-fleet-rest-api-guideline) | Adopt the fleet REST API guideline | Accepted (2026-11-01). Replaces the per-route Idempotency-Key store, the 422 answer for key reuse, the `:void` route and the bare list bodies of ADR 0002's contract. |
