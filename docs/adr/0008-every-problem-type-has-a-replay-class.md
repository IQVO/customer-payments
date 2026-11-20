# ADR 0008: Every problem type has a replay class; a retryable refusal is not stored

## Status

Accepted (2026-11-19). Narrows the sentence "a 2xx or 4xx answer is stored for 24 hours" of
[ADR 0006](./0006-adopt-the-fleet-rest-api-guideline.md); the fleet decision is the owner's of 2026-11-18
(warehouse-harness-template `docs/idempotency-replay-classes.md` and
`.claude/rules/fleet/idempotency-and-outbox.md`).

## Context

`RequireIdempotencyKey` (`internal/adapters/inbound/http/idempotency.go`) stored every answer below 500 under
the key for 24 hours, a 4xx too, and replayed it with `Idempotency-Replayed: true`. That is right for a refusal
that is final for the request (an allocation sum that does not match the total) and wrong for one a retry could
pass: a `409 concurrent-update` that was a lost race, a `404 not-found` for an order an event binds a moment
later, a `409 over-refund` judged against captured totals that grow. Each was pinned for 24 hours under a key
the client had every right to retry.

The store (`internal/adapters/outbound/postgres/idempotency.go`) claims the key row INSIDE the transaction of
the request (`claimSQL`), runs the handler behind `SAVEPOINT handler_run` and stores the answer in the same
commit, so releasing a key is the rollback a 5xx already takes (line 127): the claim and every write of the
request disappear together.

## Decision

Every problem type of the catalogue has exactly one replay class, declared on its catalogue entry
(`problemCatalogue` in `internal/adapters/inbound/http/errors.go`, the field `replay`):

| Class | Meaning | Stored and replayed |
| --- | --- | --- |
| `FINAL` | the answer is a function of the request (or of state that only moves one way) | yes |
| `OUTCOME` | the command committed state and then refused; the writes commit with the stored answer | yes |
| `RETRYABLE` | nothing committed and the answer depends on state that can change | no, the key is released |

- A 2xx is stored. A 5xx and a panic are never stored. The middleware holds no list of slugs and no status
  range beyond those two: `writeError` marks the capturing writer with the class of the problem it answers
  (`markReplay`), the capture turns that into `idempotency.Record.Release`, and the store returns before its
  `UPDATE` so the deferred rollback drops the claim and the writes (`postgres/idempotency.go` 127; the in-memory
  double does the same).
- A 4xx answer whose problem declares no class (a handler that wrote a bare status) is released like a
  RETRYABLE one: the safe fallback. The catalogue has no default class: `TestEveryProblemTypeDeclaresAReplayClass`
  fails for an entry without one, and `TestTheADRDeclaresTheClassOfEveryProblem` keeps the table below equal
  to the catalogue. `TestHandlersAnswerErrorsOnlyThroughWriteError` keeps `handlers.go` from writing a
  problem by hand.
- Releasing the key means the request is forgotten: the same body under the same key runs again, and a different
  body under the same key is a **new request**, not `409 idempotency-key-reused`.
- A replay still carries `Idempotency-Replayed: true`; the 24-hour window is unchanged.
- **A concurrent duplicate while the first ends RETRYABLE.** The duplicate blocks on the unique index of the key
  row until the first transaction ends (`claimSQL`). The first rolls back and the row never existed, so the
  blocked claim inserts it and runs the handler itself: it never replays the released refusal and never runs
  alongside the first; the copies queued behind it replay its answer. The repository contract case
  `a copy waiting on a released key runs fn itself` (memory and Postgres) and
  `TestACopyWaitingOnARetryableFirstRunsItself` pin it.
- **Released means only that the same key may be tried again by whoever sent it.** No consumer-side or
  client-side retry behaviour is added by this change.
- There is no MCP or A2A write tool (ADR 0005, ADR 0007): nothing else stores answers.

## The class of every problem type

`FINAL` and `OUTCOME` rows are stored; the reason of every `RETRYABLE` row says what can change. Four use cases
sit behind a keyed route (`AuthorizePayment`, `VoidAuthorization`, `RequestRefund`, `RetryRefund`); the others
are raised by the Kafka fact consumers and the policies only, and are classified for the day a keyed route
raises them.

| Problem type | Class | Status | Reason |
| --- | --- | --- | --- |
| `malformed-request` | FINAL | 400 | the body is not valid JSON for the operation: the request alone (`decodeJSON`, `response.go` 36-46) |
| `invalid-limit` | FINAL | 400 | the `limit` of a list; GET only, classified for completeness |
| `invalid-cursor` | FINAL | 400 | the cursor of a list; GET only |
| `unknown-query-parameter` | FINAL | 400 | a parameter the collection does not declare; GET only |
| `allocation-mismatch` | FINAL | 422 | the allocations of the request against its total and its groups (`create.go` 55 and 61): the request alone |
| `negative-amount` | FINAL | 422 | a negative total or allocation of the request (`money.go` 17-19, `create.go` 35 and 51): the request alone |
| `capture-after-expiry` | FINAL | 409 | no keyed route captures (a capture is a Kafka fact); once a hold lapsed it stays lapsed: time and the terminal state only move forward (`commands.go` 60) and a hold is renewed only before it lapses (`policies.go` 59-61). Judgement call J5 |
| `order-already-bound` | FINAL | 409 | no keyed route binds (`BindOrder` is a Kafka fact, `consume.go` 46); the binding is write-once (`commands.go` 31-40). Judgement call J5 |
| `not-found` | RETRYABLE | 404 | MIXED, J4: an authorization or a refund of the path (ids this service mints, never deleted), but also the order of `POST /refunds` (`FindByOrder`, `refund.go` 114) that an event binds to its authorization later, and an empty or overlong path id (`handlers.go` 21) |
| `payment-declined` | RETRYABLE | 422 | only the void answers it as a 422; see J1 |
| `duplicate-authorization` | RETRYABLE | 409 | no code raises it; see J2 |
| `over-capture` | RETRYABLE | 409 | no keyed route captures; unsure, J6 |
| `unknown-group` | RETRYABLE | 422 | no keyed route names a group; unsure, J6 |
| `over-refund` | RETRYABLE | 409 | `POST /refunds` is judged against the captured total of the order or group (`refund.go` 21-41, `refund/refund.go` 100-103): captures arrive by event and a FAILED refund stops counting, so the same refund passes later |
| `concurrent-update` | RETRYABLE | 409 | the optimistic-lock guard of the aggregate, or the unique index of a second live authorization of a session or of an order bound twice (`authorizations_rw.go` 130 and 140), after the use case's own `commandAttempts = 2` (`deps.go` 53, 128-149); the retry reads the new version. See J7 |
| `illegal-transition` | RETRYABLE | 409 | the aggregate's state does not allow the command now: a void of an authorization that is not AUTHORIZED (`commands.go` 120-123), a retry of a refund that is not FAILED (`refund/commands.go` 53-58); capture, expiry and the refund policies move those states |
| `validation-failed` | RETRYABLE | 422 | MIXED, J3: fields of the request (`create.go` 24-49, `refund/refund.go` 109-121), but also what the provider answered (`create.go` 87: a hold without a future expiry) |
| `refunds-disabled` | RETRYABLE | 503 | a 5xx is never stored; the ops switch can be turned back on |
| `payment-provider-unavailable` | RETRYABLE | 503 | a 5xx is never stored; the provider comes back |

### No problem type is OUTCOME

An OUTCOME needs a refusal the command answers after it committed state. None exists: every use case behind a
keyed route runs in `transact` (`deps.go` 128), whose unit of work inside the key's transaction is a nested
savepoint rolled back on any error (`unit_of_work.go` 59-66), so a use case that returns a refusal leaves none
of its own writes behind. The one write that survives a refusal is outside the database: the provider call
(`Void`, `Authorize`, `Refund`), made under a deterministic key (`deps.go` 115) that makes the provider replay
the same answer to any retry. The class exists in the catalogue and the middleware (stored, writes committed
with the answer) but nothing selects it today.

### Outside the store (no class: they never reach the replay decision)

| Problem type | Status | Reason |
| --- | --- | --- |
| `idempotency-key-required` | 400 | written by the middleware before a key exists |
| `idempotency-key-invalid` | 400 | written by the middleware before the store is called |
| `idempotency-key-reused` | 409 | written on a replay with another fingerprint; it is never the stored answer |
| `resource-not-found` | 404 | the NUL guard, before the middleware (`nul.go`) |
| `dependency-unavailable` | 503 | `/readyz`, not a keyed route |
| `internal-error` | 500 | a 5xx is never stored |

## Judgement calls (each is one line of the catalogue to flip)

- **J1. `payment-declined` is RETRYABLE.** Decided from the use cases, not from the slug. A provider decline on
  AUTHORIZE does not reach this slug: the use case keeps the decline as a DECLINED authorization, saves it,
  publishes `PaymentDeclined` and answers **201** (`authorization.go` 93-94 and 98-107, `handlers.go` 31-33,
  feature `payment_rules.feature` "A declined authorization is kept as a terminal DECLINED record"); that
  committed record is part of a 2xx, stored like any success, and its provider key carries an attempt counter
  (`authorization.go` 88: `len(earlier)+1`), so a new attempt is a new provider key. A decline of a refund is a
  FAILED refund answered 2xx (`refund.go` 74-79). The 422 is therefore raised only by the **void**
  (`authorization.go` 131): the provider call precedes `p.Void` and `Save` (134-138), so nothing is committed
  (not OUTCOME), and the key (`void:<authorizationId>:0`, `deps.go` 115) has no attempt counter. The decline
  is the provider's own definitive answer (`client.go` 179-186) and it replays it under that key, but the service's
  answer is not a function of the request: `check` reads the authorization's state first (`authorization.go`
  128), which events and policies move, and the provider stays the owner of its refusal. Storing it would copy a
  decision that is not ours for 24 hours; releasing it costs one more call that the provider answers from its
  own record. Flipping it to FINAL is one word.
- **J2. `duplicate-authorization` is RETRYABLE** (the plan's candidate said FINAL-ish). No code returns
  `ErrDuplicateAuthorization`: the repositories answer a second live authorization of a session as
  `concurrent-update` (`authorizations_rw.go` 116-141, memory `authorizations.go` 128). Were it raised, the
  retry finds the live authorization and answers 200 (`authorization.go` 62-66): a stored 409 would be false.
- **J3. `validation-failed` is RETRYABLE although most causes are the request's own.** One class per slug;
  `create.go` 87 (and `refund/commands.go` 21) turn what the provider answered into this slug. Plain field
  validation is therefore not replayed any more: the retry runs the checks again, which run before any
  provider call or write (`authorization.go` 80, `refund.go` 52-58). Splitting the provider-derived causes
  into their own problem type is a recorded follow-up, not part of this change.
- **J4. `not-found` is RETRYABLE** although the ids of an authorization and a refund are minted here: the slug
  is shared with the order of `POST /refunds`, which an event binds later. Splitting it is a recorded follow-up.
- **J5. `capture-after-expiry` and `order-already-bound` are FINAL although no keyed route raises them:** the
  code proves the cause cannot clear (time and the terminal state; a write-once binding), so a stored answer
  cannot become false.
- **J6. `over-capture` and `unknown-group` are RETRYABLE although they compare against data no code mutates**
  (the allocation amount and the group set): no keyed route raises them, which authorization is judged depends on
  a lookup by order or tracking number that events change, and when unsure the class is RETRYABLE.
- **J7. `concurrent-update` is real but rare here, and it is RETRYABLE.** Every read inside a keyed request
  locks its rows (`FOR UPDATE`, `authorizations.go` 62-64, `refunds.go` 30), so writers of one aggregate
  serialise and the version guard is a backstop; the answer surfaces from the unique indexes after both
  attempts. The integration test therefore provokes the guard with a trigger that drops the UPDATE instead of
  a timing race.

## Consequences

- After a RETRYABLE refusal the key is free: a different body under it is a new request, not
  `idempotency-key-reused`. A client that reuses a key on purpose now gets its retry executed.
- **Deterministic-key callers (report only; nothing changes in checkout here).** checkout calls this service
  with `<sessionId>:auth:<authAttempt>` and `<sessionId>:void`. A `validation-failed`, `concurrent-update`,
  `illegal-transition` or `not-found` answer to one of them is no longer pinned for 24 hours: the same key now
  executes again, which only matters to a caller that retries a refusal; the `allocation-mismatch` and
  `negative-amount` of a malformed request still replay, as before. A 503 was never stored.
- `apis/openapi.yaml` descriptions of the key, the replay header and the routes say which answers are stored and
  list the problem types that release the key; the REST reference is regenerated from them.
- Provider keys do not change: a released void or refund runs against the same deterministic provider key, so
  the provider executes it once.
