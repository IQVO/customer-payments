---
id: sequence-diagrams
title: Sequence diagrams
sidebar_label: Sequence diagrams
sidebar_position: 10
description: "Adapter to Postgres, Kafka and the payment provider for each Customer Payments flow, including the outbox relay."
---

# Sequence diagrams

Participant names are code packages. Every flow that changes state shares the
transaction pattern of [Architecture](/docs/overview/architecture): the aggregate, the
inbox claim (for consumed events) and the outbox rows are written in one transaction;
the relay publishes after the commit. A `;` never appears inside a message so the
diagrams stay valid Mermaid.

## 1. POST /payment-authorizations

```mermaid
sequenceDiagram
    autonumber
    participant C as checkout
    participant H as inbound/http
    participant I as idempotency store
    participant U as AuthorizePayment
    participant D as PaymentAuthorization
    participant P as paymentprovider
    participant DB as Postgres
    C->>H: POST /payment-authorizations<br/>Idempotency-Key
    H->>I: Do(key, method + path + body hash)
    alt stored answer for the same request
        I-->>H: stored response
        H-->>C: same status + Idempotency-Replayed true
    else stored answer of another request
        I-->>H: stored response
        H-->>C: 409 idempotency-key-reused
    else new key
        H->>U: Execute(input)
        U->>DB: FindLiveBySession
        alt a live authorization exists
            DB-->>U: authorization
            U-->>H: existing, Created false
            H-->>C: 200
        else none
            U->>D: Check(draft)
            U->>P: Authorize(key, token, total)
            P-->>U: hold or decline or unavailable
            U->>D: New(draft, outcome)
            U->>DB: Save + outbox rows (one transaction)
            U-->>H: Created true
            H->>I: store response
            H-->>C: 201
        end
    end
```

Source: `internal/adapters/inbound/http/idempotency.go` (`RequireIdempotencyKey`),
`handlers.go` (`handleAuthorize`), `internal/application/usecases/authorization.go`,
`internal/adapters/outbound/paymentprovider/client.go`. On `unavailable` the use case
returns the error and nothing is stored (503).

## 2. Outbox relay to Kafka

```mermaid
sequenceDiagram
    autonumber
    participant R as outbound/outbox Relay
    participant DB as Postgres outbox_events
    participant S as outbound/kafka Sink
    participant K as Kafka
    loop every OUTBOX_RELAY_INTERVAL
        R->>DB: Drain(limit 100, maxAttempts)
        DB-->>R: next unpublished rows in id order
        R->>S: Send(message)
        S->>K: write topic, key = subject, acks all
        K-->>S: ack
        S-->>R: ok
        R->>DB: mark published_at
    end
    Note over R,DB: a send error stops the pass,<br/>attempts += 1, last_error set.<br/>A row at the cap is skipped
```

Source: `internal/adapters/outbound/outbox/relay.go`,
`internal/adapters/outbound/postgres/outbox.go`,
`internal/adapters/outbound/kafka/sink.go`, `writer.go`. A full batch is followed
immediately by another pass.

## 3. Consuming a fulfilment fact

```mermaid
sequenceDiagram
    autonumber
    participant K as Kafka
    participant C as inbound/kafka Consumer
    participant U as usecases.Consumer
    participant DB as Postgres
    participant P as paymentprovider
    participant DLQ as topic dlq
    K->>C: FetchMessage
    C->>C: decode CloudEvents, dispatch on type
    C->>U: ApplyFact(eventID, type, fact)
    U->>DB: begin transaction
    U->>DB: Inbox.Claim(eventID)
    alt already processed
        DB-->>U: not first
    else first delivery
        U->>DB: FindByOrder
        alt order not bound
            U->>DB: Park(fact)
        else bound
            U->>P: Capture, Reduce, Void or Refund (deterministic key)
            P-->>U: result
            U->>DB: Save aggregate + outbox rows
        end
    end
    U->>DB: commit
    U-->>C: nil
    C->>K: CommitMessages (offset)
    opt transient failure 5 times or poison payload
        C->>DLQ: publish original + x-dlq headers
        C->>K: CommitMessages
    end
```

Source: `internal/adapters/inbound/kafka/kafka.go` (`consumeLoop.run`,
`handleWithDeadLetter`), `deadletter.go`, `internal/application/usecases/consume.go`
(`once`), `facts.go`. The provider call is repeated with the same key if the
transaction is retried.

## 4. Policy pass: re-authorization

```mermaid
sequenceDiagram
    autonumber
    participant L as cmd/api policy loop
    participant U as ReauthorizeBeforeExpiry
    participant DB as Postgres
    participant P as paymentprovider
    loop every POLICY_INTERVAL
        L->>U: Execute
        U->>DB: DueForReauthorization(now + 24h, 200)
        DB-->>U: authorization ids
        loop each id
            U->>DB: begin, Get(id)
            alt hold already lapsed
                U->>DB: Expire, Save, outbox
            else still valid
                U->>P: Reauthorize(key, pending amount)
                alt approved
                    U->>DB: Reauthorize, Save, outbox PaymentReauthorized
                else declined
                    U->>DB: Expire, Save, outbox PaymentDeclined + Expired
                end
            end
            U->>DB: commit
        end
    end
```

Source: `cmd/api/policies.go` (`startPolicies`, `runPolicies`),
`internal/application/usecases/policies.go` (`ReauthorizeBeforeExpiry`). A provider
outage, a concurrent update or an illegal transition skips that authorization with a
WARN and retries at the next tick.

## 5. Read path: MCP tool and A2A skill

```mermaid
sequenceDiagram
    autonumber
    participant A as agent
    participant M as cmd/mcp or cmd/a2a
    participant U as read use cases
    participant DB as Postgres
    A->>M: tools/call or SendMessage
    M->>U: GetAuthorization, ListAuthorizations, GetRefund, ListRefunds
    U->>DB: select
    DB-->>U: rows
    U-->>M: views
    M-->>A: structured result (+ text summary for A2A)
```

Source: `internal/adapters/inbound/mcp/tools.go`, `internal/adapters/inbound/a2a/skills.go`,
`cmd/mcp/main.go`, `cmd/a2a/main.go`. These binaries wire no write use case, no outbox
and no provider.
