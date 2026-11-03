---
id: 0007-agent-authority-levels-and-complete-mcp-surface
title: 0007. Agent authority levels and complete MCP surface
sidebar_label: 0007. Agent authority and MCP surface
sidebar_position: 7
description: "ADR 0007: every MCP tool and A2A skill of this context declares one authority level (all L1 — the surface is read-only), the MCP Streamable HTTP handler is stateless so it also answers the 2026-07-28 revision, and the MCP server completes its agent surface with one scoped resource (order payment status) and one operator SOP prompt."
---

# ADR 0007: Agent authority levels and complete MCP surface

## Status

Accepted (2026-11-02). Applies the fleet decision "Agentic Standard v2"
(`docs/architecture/agentic-standard.md` and the fleet rule
`agents/fleet/agent-authority.md` in IQVO/warehouse-docs) to this context.
Additive to the read-only MCP charter of this context's MCP adapter and to
[ADR 0005](./0005-a2a-inbound-adapter.md) (A2A); changes no tool or skill
name, argument or result, and touches nothing in
[ADR 0003](./0003-cloudevents-envelope-and-type-catalogue.md) (the CloudEvents
envelope and type catalogue stay as they are).

## Context

The fleet is standardising what an agent may do with a surface before any
gate is configured: every MCP tool and every A2A skill must declare exactly
one **authority level** (`L1` reads facts, `L2` recommends, `L3` writes with
accountability, `L4` writes without a per-call human gate and needs an ADR of
the owning context). This context's agent surface is entirely read-only — the
four MCP tools `get_payment_authorization`, `list_payment_authorizations`,
`get_refund`, `list_refunds` and the two A2A skills `get_payment_status`,
`get_refund_status` — and no ADR of this context grants any write over MCP or
A2A, so nothing here can be L2 (no recommendation tool exists), L3 or L4.

Separately, the fleet's MCP servers built the Streamable HTTP handler with
its default (stateful) options, which advertises only the 2025-11-25 revision
and older, so a client speaking the 2026-07-28 per-request handshake was
refused with `-32022`. Finally, the surface was tools-only: no resource and
no prompt, so a client could not load context data without a tool call and
had no standard operating procedure for using the reads.

## Decision

### Authority levels, declared where the surface is registered

Every MCP tool declares its level in its `Meta` under
`com.warehouse/authority` (`authorityMeta(authorityLn)` in
`internal/adapters/inbound/mcp/authority.go`, returned verbatim in
`tools/list` `_meta`); every A2A skill declares it as the tag
`authority:L<n>` on its Agent Card entry.

| Surface | Name | Level | Why |
| --- | --- | --- | --- |
| MCP | `get_payment_authorization` | `L1` | reads facts, changes nothing |
| MCP | `list_payment_authorizations` | `L1` | reads facts, changes nothing |
| MCP | `get_refund` | `L1` | reads facts, changes nothing |
| MCP | `list_refunds` | `L1` | reads facts, changes nothing |
| A2A | `get_payment_status` | `L1` | reads; keeps its `read` tag |
| A2A | `get_refund_status` | `L1` | reads; keeps its `read` tag |

`L4` is not granted here: no ADR of this context lets an agent move money
without a human, and authorizing, voiding and refunding stay on REST and on
the event consumers. The level is a label for callers and reviewers; **it is
not access control** and nothing in this context decides anything from it
(the surface stays unauthenticated, like REST, per the fleet decision).

Two sensors enforce it. `internal/architecture/mcp_authority_fitness_test.go`
(the fleet's portable static sensor, template PR 47) fails the build when any
`mcp.Tool` literal misses a readable `Meta` declaration, any `a2a.AgentSkill`
literal misses its `authority:L<n>` tag, or a declared level contradicts the
read-only annotations; `TestToolSurface` additionally pins at runtime that
every advertised tool declares exactly `L1`. The golden
`testdata/tool_registry.golden.json` carries the `_meta` authority entry, so
a level can only change through a reviewed golden diff.

### Stateless MCP

`mcp.NewStreamableHTTPHandler` now receives
`&mcp.StreamableHTTPOptions{Stateless: true}`. No `Mcp-Session-Id` is issued
and the server answers `server/discover` for the 2026-07-28 revision, while a
legacy `initialize` (for example `2025-06-18`) still answers 200, so existing
session clients keep working. Nothing in this adapter relied on cross-call
session state; a tool that ever needs it takes an explicit, server-minted
handle as an argument. `stateless_test.go` pins the three wire facts with raw
POSTs (no initialize, no session id, the per-request `_meta` of the new
revision), and reverting the options to `nil` makes the `server/discover` and
`tools/list` probes fail with exactly the fleet-spiked `-32022` — proven on
this branch before the PR.

### One resource, one prompt

The MCP server now also offers:

- the resource template
  `warehouse://customer-payments/orders/{orderId}/payment-status` — a scoped
  read model this context owns: every authorization and every refund of ONE
  order, built from the same `ListAuthorizations` and `ListRefunds` use cases
  the tools and REST use, JSON, bounded by the same per-order caps, never a
  dump of a table. The `orderId` segment is untrusted: a malformed URI or a
  segment outside the 1..128 id guard is rejected, never defaulted.
- the prompt `investigate_order_payment` — this context's operator SOP for
  reading where an order's payment stands and how its refunds went: which
  tools to call, what each state means, when to escalate to a human (a FAILED
  refund is a provider refusal the operator owns) and what "done" means. Its
  one argument (`orderId`) is typed and optional; the prompt names tools but
  never calls them and hides no write.

Resources and prompts are read-only by nature and are `L1` facts of the
surface; they carry no authority `Meta` (the fleet contract reserves that for
tools). The charter stays read-only.

## Consequences

- An agent host can gate on the declared level without reading source: every
  tool and skill here says `L1` on the wire (`_meta` for MCP, a card tag for
  A2A).
- A 2026-07-28 client works against `cmd/mcp` with no session round-trip, and
  legacy session clients are unaffected (pinned by test).
- The registry golden diff in this PR is exactly the four `_meta` blocks; any
  future level change is a reviewed golden edit.
- Adding any non-read tool now requires declaring `L3` (or an L4-granting ADR
  plus a `//authority:L4 <adr>` comment) and would still be refused by this
  context's write-verb governance test — the charter amendment comes first.
