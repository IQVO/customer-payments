package mcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Authority levels (Agentic Standard v2, AS-1; docs/adr/0007). Every
// registered tool declares exactly one of these in its Meta under
// authorityMetaKey, following the fleet rule agent-authority
// (IQVO/warehouse-docs agents/fleet/agent-authority.md). This context's MCP
// surface is read-only by charter, so every tool here is L1; the other levels
// exist because the fleet contract names all four, and any future tool must
// pick from them deliberately (a write tool would be L3, and L4 needs an ADR
// of this context — none grants it).
const (
	// authorityMetaKey is the tools/list _meta key carrying a tool's level.
	authorityMetaKey = "com.warehouse/authority"
	// authorityL1 reads facts and changes nothing.
	authorityL1 = "L1"
	// authorityL2 is a pure recommendation: computes a proposal, changes nothing.
	authorityL2 = "L2"
	// authorityL3 mutates state; the caller is accountable.
	authorityL3 = "L3"
	// authorityL4 mutates state without a per-call human gate; only an ADR of
	// this context can grant it (none does).
	authorityL4 = "L4"
)

// authorityMeta is the Meta a tool registers to declare its authority level.
// It is returned verbatim in tools/list _meta.
func authorityMeta(level string) mcp.Meta { return mcp.Meta{authorityMetaKey: level} }
