---
paths:
  - "internal/adapters/**/kafka/**"
  - "internal/adapters/outbound/events/**"
  - "apis/asyncapi*"
---

# Rule: CloudEvents 1.0 on every Kafka message (retail network)

This repository belongs to the retail network, a separate organization from the warehouse-systems fleet. It follows the fleet's
CloudEvents discipline with the network's own namespace (warehouse-docs `network/model/model.yaml`, `conventions.cloudevents`).

- CloudEvents 1.0 (structured JSON) is MANDATORY on every message this service publishes or consumes. No flat envelope, no dual-write, no envelope toggle.
- `type` = `com.retailnetwork.<tier>.<context>.<entity>.<Event>`; tier is `commerce`, `logistics` or `partner` for the network's own contexts and `storefront` for the storefront domain's contexts (a business-area label, not the Core/Supporting/Generic verdict). `<entity>` is the raising aggregate, lowercase, no separators.
- `source` = `/retailnetwork/<context>`; `dataschema` = `urn:retailnetwork:<context>:<stream>:<Event>:v<n>`; `subject` and the Kafka key = the aggregate root id.
- Topics: `retailnetwork.<context>.events` (integration) and `retailnetwork.<context>.analytics`. An event marked analytics-only goes ONLY to the analytics topic. No topic is shared with the warehouse-systems fleet and this service never consumes a `warehouse.*` topic.
- Use the service's own helper `internal/adapters/kafka/cloudevents` (New / Decode / ContentTypeHeader) for every publish and consume; never hand-build the envelope.
- A consumer rejects anything that is not a valid CloudEvents 1.0 envelope and dispatches on the FULL type string, never a suffix.
- Changing an event contract is a cross-repo change: update the AsyncAPI document, the owning ADR, warehouse-docs `network/model/model.yaml` and every consumer together.
