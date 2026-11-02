package mcp

import "testing"

// TestAuthorityMetaPinsEveryLevel keeps the declaration helper honest for all
// four fleet levels (only L1 is used by this read-only surface today). A stub
// helper, or one that stopped setting the fleet key, fails here and in the
// architecture sensor (TestEveryMCPToolDeclaresAuthority).
func TestAuthorityMetaPinsEveryLevel(t *testing.T) {
	for _, level := range []string{authorityL1, authorityL2, authorityL3, authorityL4} {
		meta := authorityMeta(level)
		if got := meta[authorityMetaKey]; got != level {
			t.Fatalf("authorityMeta(%q)[%q] = %q, want %q", level, authorityMetaKey, got, level)
		}
		if len(meta) != 1 {
			t.Fatalf("authorityMeta(%q) carries more than the authority key: %v", level, meta)
		}
	}
}
