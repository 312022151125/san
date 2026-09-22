package agent_test

import (
	"testing"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/tool"
	_ "github.com/genai-io/san/internal/tool/register" // register all built-in tools
)

// TestBuildParamsBatchEnabledPropagatestoSchemas verifies the full propagation
// chain: BuildParams.BatchEnabled=true → Schemas() → Agent tool schema contains
// tasks[] and context fields (OMP2 Task Batch).
//
// This is the critical wiring test: if ReconfigureAgentTool sets BatchEnabled=true
// but the path through tool.Set / SchemaOptions is broken, the model never sees
// the batch parameters and task-batch calls fail silently.
func TestBuildParamsBatchEnabledPropagatestoSchemas(t *testing.T) {
	enabled := agent.BuildParams{BatchEnabled: true}
	disabled := agent.BuildParams{BatchEnabled: false}

	agentSchemaFor := func(p agent.BuildParams) map[string]any {
		for _, s := range p.Schemas() {
			if s.Name == tool.ToolAgent {
				def, ok := s.Definition.(map[string]any)
				if !ok {
					t.Fatalf("Agent schema definition is not a map")
				}
				return def
			}
		}
		t.Fatalf("Agent tool not found in Schemas()")
		return nil
	}

	propsOf := func(def map[string]any) map[string]any {
		props, ok := def["properties"].(map[string]any)
		if !ok {
			t.Fatalf("Agent schema has no properties map")
		}
		return props
	}

	// When BatchEnabled=true the tasks[] and context fields must be present.
	enabledProps := propsOf(agentSchemaFor(enabled))
	if _, ok := enabledProps["tasks"]; !ok {
		t.Error("BatchEnabled=true: tasks[] must be present in Agent schema")
	}
	if _, ok := enabledProps["context"]; !ok {
		t.Error("BatchEnabled=true: context must be present in Agent schema")
	}

	// When BatchEnabled=false the batch fields must be absent (zero overhead).
	disabledProps := propsOf(agentSchemaFor(disabled))
	if _, ok := disabledProps["tasks"]; ok {
		t.Error("BatchEnabled=false: tasks[] must not be present in Agent schema")
	}
	if _, ok := disabledProps["context"]; ok {
		t.Error("BatchEnabled=false: context must not be present in Agent schema")
	}
}

// TestBuildParamsSchemasIncludesBatchTool verifies that BuildParams.Schemas()
// includes the Batch tool (Tura-style Command Batch) in the returned schema list.
func TestBuildParamsSchemasIncludesBatchTool(t *testing.T) {
	p := agent.BuildParams{}
	for _, s := range p.Schemas() {
		if s.Name == tool.ToolBatch {
			return
		}
	}
	t.Error("Batch tool must appear in BuildParams.Schemas()")
}
