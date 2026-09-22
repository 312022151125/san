package subagent

import (
	"slices"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
	_ "github.com/genai-io/san/internal/tool/register" // registers built-in schemas
)

// TestAdvisorToolGating pins the optional-capability wiring for the advisor:
// its compiled-in allow list carries WebSearch + recall + reflect (never
// retain), the memory schemas surface only when the caller injects them as
// ExtraTools (memory backend on), and explore mode's safe-tool filter admits
// all three reads.
func TestAdvisorToolGating(t *testing.T) {
	cfg := BuiltinAdvisorConfig()

	// The allow list names the optional tools statically: an absent schema
	// simply never matches, so the entries cost nothing when memory is off.
	if !slices.Contains(cfg.AllowTools.Names(), tool.ToolWebSearch) {
		t.Error("advisor allow list must include WebSearch")
	}
	if !slices.Contains(cfg.AllowTools.Names(), tool.ToolRecall) ||
		!slices.Contains(cfg.AllowTools.Names(), tool.ToolReflect) {
		t.Error("advisor allow list must include recall and reflect")
	}
	if slices.Contains(cfg.AllowTools.Names(), tool.ToolRetain) {
		t.Error("advisor must never be allowed to retain (memory writes are parent-only)")
	}

	schemaNames := func(extra []core.ToolSchema) []string {
		set := newAgentToolSet(cfg.AllowTools.Names(), cfg.DenyTools.BareNames(), nil, nil, extra, false)
		got := filterSchemasForPermission(set.Tools(), cfg.PermissionMode, cfg.AllowTools)
		names := make([]string, 0, len(got))
		for _, s := range got {
			names = append(names, s.Name)
		}
		return names
	}

	// Memory off (nil ExtraTools): WebSearch resolves from the built-in
	// order; recall/reflect match nothing and stay out.
	off := schemaNames(nil)
	if !slices.Contains(off, tool.ToolWebSearch) {
		t.Errorf("WebSearch must be visible with memory off, got %v", off)
	}
	if slices.Contains(off, tool.ToolRecall) || slices.Contains(off, tool.ToolReflect) {
		t.Errorf("recall/reflect must be absent with memory off, got %v", off)
	}

	// Memory on: injected schemas pass the whitelist + explore safe-tool
	// filter. retain, if ever injected, is dropped by the parent-only filter.
	on := schemaNames([]core.ToolSchema{
		{Name: tool.ToolRecall}, {Name: tool.ToolReflect}, {Name: tool.ToolRetain},
	})
	for _, want := range []string{tool.ToolWebSearch, tool.ToolRecall, tool.ToolReflect} {
		if !slices.Contains(on, want) {
			t.Errorf("%s must be visible with memory on, got %v", want, on)
		}
	}
	if slices.Contains(on, tool.ToolRetain) {
		t.Errorf("retain must be filtered out for subagents (parent-only), got %v", on)
	}
}
