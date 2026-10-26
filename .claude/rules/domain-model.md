# Domain model: ubiquitous language, aggregates, events, use cases

## Ubiquitous Language (use these exact names)

- **PaymentAuthorization** — the hold placed at the provider for one checkout session, split into
  one allocation per shipment group; allocations always sum to the authorized total.
- **Allocation** — a shipment group's share (`groupNo`, `amountMinor`, state PENDING|CAPTURED|RELEASED,
  tracking number set at capture). States of the authorization are DERIVED from its allocations.
- **Capture / Release** — move an allocation's money (capture, when its group ships) or give it
  back (release, on cancellation, unsourceable, void or expiry). Never capture past `expiresAt`.
- **Refund** — money returned for a captured group (reasons LOST, RETURNED_TO_SENDER, MANUAL):
  REQUESTED then SUCCEEDED or FAILED; a FAILED one is retried up to 5 attempts.
- **Parked fact** — a fulfilment or package fact for an order not yet bound to its authorization;
  applied on bind, purged after 30 days, dead-lettered with an alert after 24 hours.
- **Provider** — the payment provider behind an anti-corruption port; only PSP tokens, never a card number.

## Aggregates

- **PaymentAuthorization** (`internal/domain/paymentauthorization`): enforces allocation sum
  (ErrAllocationMismatch), no over-capture, no capture after expiry, unknown group, one per session.
- **Refund** (`internal/domain/refund`): enforces the state machine, the 5-attempt retry cap and
  that refunds (REQUESTED plus issued, never FAILED) never exceed the captured total (ErrOverRefund).
- Shared value types (money, ids, UTC time) live in `internal/domain/shared`.

## Domain events

- `PaymentAuthorized`, `PaymentCaptured`, `PaymentReleased` (CANCELLED|UNSOURCEABLE|VOIDED|EXPIRED),
  `PaymentAuthorizationExpired`, `PaymentDeclined`, `PaymentReauthorized`, `RefundIssued`, `RefundFailed`;
  payloads in `docs/adr/0003-cloudevents-envelope-and-type-catalogue.md`.

## Key use cases (`internal/application/usecases`)

- `AuthorizePayment` / `VoidAuthorization` — authorize (replay returns the existing one) and void via the provider port.
- `Consumer.CheckoutCompleted` — BindOrderOnCheckoutCompleted: bind the order id, apply parked facts.
- `Consumer.ApplyFact` — CaptureOnShipment, ReleaseOnCancellation, RefundOnLostPackage, RefundOnReturnToSender.
- `RequestRefund` / `RetryRefund` — manual refund and retry of a FAILED one.
- Policies (`policies.go`) — ReauthorizeBeforeExpiry, VoidUnboundAuthorizations, parked-fact purge and escalation.
