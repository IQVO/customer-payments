---
id: ddd-artifacts
title: DDD artifacts
sidebar_label: DDD artifacts
sidebar_position: 4
description: "Index of the ddd-crew artifact pack for Customer Payments, each page checked against the code on develop."
---

# DDD artifacts (ddd-crew)

The artifact pack follows the [ddd-crew](https://github.com/ddd-crew) tools.
Each page is checked against the code on this branch; every Mermaid diagram
carries a `Source:` line naming the code files it was derived from. The design
model lives in warehouse-docs
([Customer Payments context pages](https://iqvo.github.io/warehouse-docs/storefront/contexts/customer-payments));
where the built service differs from the model, the pages here follow the code.

| Page | What it holds | ddd-crew tool |
| --- | --- | --- |
| [Core domain chart](/docs/ddd/core-domain-chart) | Why the context is Generic: differentiation against complexity | Core Domain Charts |
| [Bounded context canvas](/docs/ddd/bounded-context-canvas) | Purpose, roles, every inbound and outbound message, decisions, metrics, open questions | Bounded Context Canvas v5 |
| [Context map](/docs/ddd/context-map) | Every neighbour, direction and pattern, including the provider ACL | Context Mapping |
| [Aggregate design canvas](/docs/ddd/aggregate-design-canvas) | `PaymentAuthorization` and `Refund`: states, invariants, commands, events | Aggregate Design Canvas v1.1 |
| [Domain message flow](/docs/ddd/domain-message-flow) | Authorize, capture, parked facts, release, refund, re-authorize | Domain Message Flow Modelling |
| [EventStorming](/docs/ddd/eventstorming) | Process boards and hotspots | EventStorming cheat sheet |
| [Ubiquitous language](/docs/ddd/ubiquitous-language) | Terms and code identifiers | Bounded Context Canvas (language section) |
| [Class diagram](/docs/ddd/class-diagram) | Domain model, ports and adapters as built | UML |
| [Entity relationship](/docs/ddd/entity-relationship) | The Postgres schema of the single migration | ER |
| [Sequence diagrams](/docs/ddd/sequence-diagrams) | Adapter to Postgres, Kafka and provider for each flow | UML |
| [Domain events](/docs/ddd/domain-events) | Published and consumed events, pointers to the catalogue | Message catalogue |

Also in this section: [Use cases](/docs/ddd/use-cases) (one section per command,
query, consumer and policy) and
[Subdomain classification](/docs/ddd/subdomain-classification).
