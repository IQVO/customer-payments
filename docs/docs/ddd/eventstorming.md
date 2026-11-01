---
id: eventstorming
title: EventStorming
sidebar_label: EventStorming
sidebar_position: 6
description: "Process-level EventStorming boards for Customer Payments with the hotspots that remain open after checking the code."
---

# EventStorming

Process-level boards in the ddd-crew
[EventStorming](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet)
notation. Every sticky comes from the code. Neighbour events are orange stickies
prefixed with the producer (`OS` order-sourcing, `ST` shipping-and-tracking, `CK`
checkout).

| Colour | Sticky |
| --- | --- |
| Pale yellow | Actor |
| Blue | Command |
| Yellow | Aggregate |
| Orange | Domain event |
| Lilac | Policy |
| Green | Read model |
| Pink | External system |

## 1. Authorize at checkout

```mermaid
flowchart LR
    CK["checkout"]:::external
    C0["AuthorizePayment<br/>POST /payment-authorizations"]:::command
    C1["VoidAuthorization<br/>POST .../voids"]:::command
    PSP["payment provider<br/>ACL port"]:::external
    G1["PaymentAuthorization"]:::aggregate
    E1["PaymentAuthorized"]:::event
    E2["PaymentDeclined"]:::event
    E3["PaymentReleased<br/>reason VOIDED"]:::event
    AN([analytics]):::actor
    CK --> C0
    CK -->|only after a definitive<br/>rejection| C1
    C0 -->|Authorize<br/>allocation-mismatch| PSP
    PSP --> G1
    C1 -->|Void| PSP
    C1 --> G1
    G1 --> E1
    G1 --> E2
    G1 --> E3
    E1 --> AN
    E2 --> AN
    E3 --> AN
    classDef actor fill:#fff59d,stroke:#b59f00,color:#000
    classDef command fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef aggregate fill:#ffec99,stroke:#e67700,color:#000
    classDef event fill:#ffc078,stroke:#d9480f,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
```

Source: `internal/application/usecases/authorization.go`,
`internal/domain/paymentauthorization/events.go`.

## 2. Bind the order and apply parked facts

```mermaid
flowchart LR
    OS["OS FulfillmentShipped,<br/>Cancelled, Unsourceable"]:::event
    ST["ST PackageLost,<br/>PackageReturned"]:::event
    CK["checkout"]:::external
    E4["CK CheckoutCompleted"]:::event
    RM0["parked facts"]:::readmodel
    P1["bind on CheckoutCompleted"]:::policy
    C2["BindOrder"]:::command
    G1["PaymentAuthorization"]:::aggregate
    P2["apply parked facts"]:::policy
    PU["escalate after 24 h,<br/>purge after 30 days"]:::policy
    OS -->|order not bound| RM0
    ST -->|order not bound| RM0
    CK --> E4
    E4 --> P1
    P1 --> C2
    C2 --> G1
    G1 --> P2
    RM0 --> P2
    RM0 --> PU
    classDef command fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef aggregate fill:#ffec99,stroke:#e67700,color:#000
    classDef event fill:#ffc078,stroke:#d9480f,color:#000
    classDef policy fill:#d0bfff,stroke:#6741d9,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
```

Source: `internal/application/usecases/consume.go`, `facts.go` (`replayParked`),
`policies.go` (`MaintainParkedFacts`), `internal/application/parked`.

## 3. Capture and release

```mermaid
flowchart LR
    OS["OS FulfillmentShipped"]:::event
    OC["OS FulfillmentCancelled"]:::event
    OU["OS FulfillmentUnsourceable"]:::event
    P3["capture on shipment"]:::policy
    P4["release on cancellation"]:::policy
    P4B["release on unsourceable"]:::policy
    C3["Capture"]:::command
    C4["Release"]:::command
    PSP["payment provider<br/>Capture"]:::external
    PSR["payment provider<br/>ReduceAuthorization or Void"]:::external
    G1["PaymentAuthorization<br/>allocations,<br/>tracking numbers set at capture"]:::aggregate
    E5["PaymentCaptured"]:::event
    E6["PaymentReleased"]:::event
    AN([analytics]):::actor
    OS --> P3
    OC --> P4
    OU --> P4B
    P3 --> C3
    P4 --> C4
    P4B --> C4
    C3 -->|over-capture,<br/>capture-after-expiry,<br/>unknown-group| PSP
    PSP --> G1
    C4 -->|unknown-group| PSR
    PSR --> G1
    G1 --> E5
    G1 --> E6
    E5 --> AN
    E6 --> AN
    classDef actor fill:#fff59d,stroke:#b59f00,color:#000
    classDef command fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef aggregate fill:#ffec99,stroke:#e67700,color:#000
    classDef event fill:#ffc078,stroke:#d9480f,color:#000
    classDef policy fill:#d0bfff,stroke:#6741d9,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
```

Source: `internal/application/usecases/facts.go` (`captureGroup`, `releaseGroup`,
`shrinkHold`), `internal/domain/paymentauthorization/commands.go`.

## 4. Refunds

```mermaid
flowchart LR
    ST["ST PackageLost"]:::event
    SR["ST PackageReturned"]:::event
    OPS["storefront ops"]:::actor
    P5["refund on lost package"]:::policy
    P6["refund on return to sender"]:::policy
    C5["RequestRefund<br/>POST /refunds"]:::command
    G2["Refund"]:::aggregate
    PSP["payment provider<br/>Refund"]:::external
    C6["RecordSuccess or RecordFailure"]:::command
    E7["RefundIssued"]:::event
    E8["RefundFailed"]:::event
    C9["RetryRefund<br/>FAILED to REQUESTED,<br/>max 5 attempts"]:::command
    ST --> P5
    SR --> P6
    P5 -->|reason LOST| C5
    P6 -->|reason RETURNED_TO_SENDER| C5
    OPS -->|reason MANUAL| C5
    C5 -->|over-refund| G2
    G2 --> PSP
    PSP --> C6
    C6 --> G2
    G2 --> E7
    G2 --> E8
    E8 --> C9
    C9 --> G2
    classDef actor fill:#fff59d,stroke:#b59f00,color:#000
    classDef command fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef aggregate fill:#ffec99,stroke:#e67700,color:#000
    classDef event fill:#ffc078,stroke:#d9480f,color:#000
    classDef policy fill:#d0bfff,stroke:#6741d9,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
```

Source: `internal/application/usecases/refund.go`, `facts.go` (`refundPackage`),
`internal/domain/refund/refund.go`.

## 5. Clock policies

```mermaid
flowchart LR
    T([Clock]):::actor
    P7["reauthorize-before-expiry<br/>24 h before expiresAt"]:::policy
    P8["void-unbound-authorizations<br/>no CheckoutCompleted after 24 h"]:::policy
    P9["process-requested-refunds<br/>REQUESTED for 30 s"]:::policy
    C7["Reauthorize"]:::command
    C1["Void"]:::command
    C8["Expire"]:::command
    PSP["payment provider<br/>Reauthorize, Void, Refund"]:::external
    G1["PaymentAuthorization"]:::aggregate
    E9["PaymentAuthorizationExpired"]:::event
    E10["PaymentReauthorized"]:::event
    E11["PaymentDeclined<br/>reason REAUTH_DECLINED"]:::event
    T --> P7
    T --> P8
    T --> P9
    P7 --> C7
    P8 --> C1
    C7 --> PSP
    C1 --> PSP
    PSP --> G1
    C7 -->|provider decline| C8
    C8 --> G1
    G1 --> E9
    G1 --> E10
    G1 --> E11
    classDef actor fill:#fff59d,stroke:#b59f00,color:#000
    classDef command fill:#a5d8ff,stroke:#1971c2,color:#000
    classDef aggregate fill:#ffec99,stroke:#e67700,color:#000
    classDef event fill:#ffc078,stroke:#d9480f,color:#000
    classDef policy fill:#d0bfff,stroke:#6741d9,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
```

Source: `cmd/api/policies.go` (`policyRunners`),
`internal/application/usecases/policies.go`, `refund.go` (`ProcessRequestedRefunds`).

## Hotspots

The model's twelve hotspots (H1 to H12) are resolved in the code; what remains open:

| # | Hotspot | Evidence |
| --- | --- | --- |
| 1 | **No consumer of the eight events.** All are analytics-only by contract; nobody reacts. | `apis/asyncapi.yaml`; [Integration](/docs/ecosystem/integration) |
| 2 | **No alerting** on `payments.authorizations.decided`, parked facts, unpublished outbox rows or a failed refund; only ERROR log lines (`ALERT refund failed for good`, `ALERT fact for an unbound order dead-lettered`). | [Observability](/docs/operations/observability#dashboards-and-alerts) |
| 3 | **A fact for a tracking number no capture carries** is parked until 24 h, then escalated; a lost package whose shipment fact never arrived is never refunded automatically. | `facts.go` (`refundPackage`), `policies.go` |
| 4 | **OpenAPI drift**: `RELEASED`, `GET /refunds/{refundId}` and `POST /refunds/{refundId}:retry` are not in `apis/openapi.yaml` (the `200` replay is). | [API overview](/docs/api-reference#spec-versus-code) |
| 5 | **Expiry without re-authorization**: a provider outage longer than the 24 h lead lets a hold lapse; the next pass expires the authorization without asking the provider. | `policies.go` (`renew`) |
