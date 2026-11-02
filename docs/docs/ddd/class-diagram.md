---
id: class-diagram
title: Class diagram
sidebar_label: Class diagram
sidebar_position: 8
description: "UML class diagrams of Customer Payments' domain model, its events, and the hexagonal ports and adapters as built."
---

# Class diagram

UML of the `internal/domain/**` packages and the hexagonal ports and adapters as
built. Fields are unexported in Go and shown with `-`; methods with `+`. Exported
domain types: `paymentauthorization`: `ID`, `State`, `AllocState`, `Allocation`,
`AllocationInput`, `Draft`, `Outcome`, `PaymentAuthorization`, `Snapshot` and the
events; `refund`: `ID`, `State`, `Reason`, `Request`, `Limits`, `Refund`, `Snapshot` and
the events.

## 1. The PaymentAuthorization aggregate

```mermaid
classDiagram
    direction TB
    class PaymentAuthorization {
        <<AggregateRoot>>
        -id ID
        -sessionID string
        -orderID string
        -currency string
        -total int64
        -allocations Allocation[]
        -providerAuthorizationID string
        -expiresAt time.Time
        -reauthorizations int
        -version int64
        +New(Draft, Outcome, now)$ PaymentAuthorization
        +BindOrder(orderID) bool, error
        +Capture(groupNo, amount, trackings, captureID, now) Event[], error
        +Release(groupNo, reason, now) Event[], error
        +Void(now) Event[], error
        +Expire(declined, now) Event[], error
        +Reauthorize(holdID, expiresAt, now) Event[], error
        +State() State
    }
    class State {
        <<Enumeration>>
        AUTHORIZED
        PARTIALLY_CAPTURED
        CAPTURED
        RELEASED
        VOIDED
        EXPIRED
        DECLINED
    }
    class Allocation {
        <<ValueObject>>
        +GroupNo int
        +AmountMinor int64
        +State AllocState
        +CapturedMinor int64
        +TrackingNumbers string[]
    }
    class AllocState {
        <<Enumeration>>
        PENDING
        CAPTURED
        RELEASED
    }
    PaymentAuthorization --> State : derived
    PaymentAuthorization *-- "1..*" Allocation : allocations
    Allocation --> AllocState : state
```

Source: `internal/domain/paymentauthorization/paymentauthorization.go`, `create.go`,
`commands.go`, `restore.go`.

## 2. The Refund aggregate

```mermaid
classDiagram
    direction TB
    class Refund {
        <<AggregateRoot>>
        -req Request
        -state State
        -attempts int
        -failureCode string
        -providerRefundID string
        -version int64
        +New(Request, Limits, now)$ Refund
        +RecordSuccess(providerRefundID, now) Event[], error
        +RecordFailure(code, now) Event[], error
        +Retry() error
        +Exhausted() bool
    }
    class Reason {
        <<Enumeration>>
        LOST
        RETURNED_TO_SENDER
        MANUAL
    }
    class State {
        <<Enumeration>>
        REQUESTED
        SUCCEEDED
        FAILED
    }
    class Limits {
        <<ValueObject>>
        +CapturedMinor int64
        +RefundedMinor int64
        +OrderCapturedMinor int64
        +OrderRefundedMinor int64
    }
    Refund --> Reason : reason
    Refund --> State : state
    Refund ..> Limits : checked on New
```

Source: `internal/domain/refund/refund.go`, `commands.go`,
`internal/application/usecases/refund.go` (`limitsFor`).

## 3. Domain events

```mermaid
classDiagram
    direction LR
    class PaymentAuthorized {
        <<DomainEvent>>
        +AuthorizationID ID
        +CheckoutSessionID string
        +Currency string
        +TotalMinor int64
        +Allocations AllocationAmount[]
        +ExpiresAt time.Time
    }
    class PaymentDeclined {
        <<DomainEvent>>
        +AuthorizationID ID
        +CheckoutSessionID string
        +Reason string
    }
    class PaymentCaptured {
        <<DomainEvent>>
        +AuthorizationID ID
        +OrderID string
        +GroupNo int
        +AmountMinor int64
    }
    class PaymentReleased {
        <<DomainEvent>>
        +AuthorizationID ID
        +OrderID string
        +GroupNo int
        +AmountMinor int64
        +Reason string
    }
    class PaymentAuthorizationExpired {
        <<DomainEvent>>
        +AuthorizationID ID
        +OrderID string
        +ExpiredAt time.Time
    }
    class PaymentReauthorized {
        <<DomainEvent>>
        +AuthorizationID ID
        +OrderID string
        +ExpiresAt time.Time
        +ProviderAuthorizationID string
    }
    class RefundIssued {
        <<DomainEvent>>
        +RefundID ID
        +OrderID string
        +AuthorizationID string
        +GroupNo int
        +AmountMinor int64
        +Reason Reason
    }
    class RefundFailed {
        <<DomainEvent>>
        +RefundID ID
        +Code string
        +Attempts int
        +Final bool
    }
```

Source: `internal/domain/paymentauthorization/events.go`,
`internal/domain/refund/events.go`.

## 4. Hexagonal ports and adapters

```mermaid
flowchart TB
    subgraph IN["internal/adapters/inbound"]
        HTTP["http<br/>REST, problem+json,<br/>Idempotency-Key"]
        KC["kafka<br/>checkout, order-sourcing,<br/>shipping-and-tracking consumers"]
        MCP["mcp<br/>4 read tools"]
        A2A["a2a<br/>2 read skills, Agent Card"]
    end
    POL["cmd/api policy loop<br/>reauthorize, void unbound,<br/>process refunds, parked facts"]
    APP["<b>internal/application/usecases</b><br/>AuthorizePayment, VoidAuthorization<br/>RequestRefund, RetryRefund<br/>Consumer, policies, queries"]
    DOM["<b>internal/domain</b><br/>paymentauthorization, refund, shared"]
    PORTS["<b>internal/application/ports</b><br/>repositories, UnitOfWork, EventOutbox,<br/>Inbox, ParkedFacts, PaymentProvider,<br/>Escalator, IdempotencyStore, Clock, IDs"]
    subgraph OUT["internal/adapters/outbound"]
        A1["postgres<br/>repositories, outbox, inbox,<br/>parked facts, idempotency, UoW"]
        A2["outbox + kafka<br/>relay, encoder, escalator"]
        A3["paymentprovider<br/>ACL adapter over HTTP"]
        A4["telemetry, clock, ids"]
    end
    HTTP --> APP
    KC --> APP
    MCP --> APP
    A2A --> APP
    POL --> APP
    APP --> DOM
    APP --> PORTS
    PORTS --- A1
    PORTS --- A3
    PORTS --- A4
    A1 -. outbox rows .-> A2
    classDef outblue fill:#e7f5ff,stroke:#1c7ed6,color:#000
    class A3 outblue
```

Source: `cmd/api/main.go` (`buildAdapters`, `buildServer`), `internal/application/usecases/deps.go`
(`Deps`), `internal/application/ports/ports.go`, the package list under
`internal/adapters/`. The memory adapter and `repocontract` are test support.

**The Anti-Corruption Layer.** The `PaymentProvider` port speaks the domain's language
(a PSP token, an amount in minor units, a deterministic key) and returns domain
results (a hold with an expiry, a decline code). Everything the provider names
differently stays in `internal/adapters/outbound/paymentprovider`; the simulator is its
stand-in and a real provider replaces only this package. The only outbound client of the
service is this adapter. `cmd/mcp` and `cmd/a2a` wire only the repositories and the
read use cases.
