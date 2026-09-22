package agent

import (
	"strings"
	"testing"
)

func TestAgentSchemaEmbedsDirectory(t *testing.T) {
	directory := "Available agents for the Agent tool:\n\n- project-reviewer: General multi-step review agent\n  Tools: Read, Bash(git diff*)\n- plugin:browser-user: Uses a browser\n  Tools: WebFetch"

	schema := agentSchema(directory, false)
	if !strings.Contains(schema.Description, "project-reviewer") {
		t.Error("Agent description should embed the directory body when supplied")
	}
	if !strings.Contains(schema.Description, "plugin:browser-user") {
		t.Error("Agent description should list every directory entry")
	}
	if !strings.Contains(schema.Description, "Available agent definitions") {
		t.Error("Agent description should label the available definitions")
	}
}

func TestAgentSchemaOmitsDirectoryWhenEmpty(t *testing.T) {
	schema := agentSchema("", false)
	if strings.Contains(schema.Description, "Available agent definitions") {
		t.Error("empty directory should not produce an available-agents block")
	}
	if strings.Contains(schema.Description, "Omit name") {
		t.Error("schema should not prescribe omitted-name behavior")
	}
}

// TestAgentToolSchemaMatchesEmptyDirectory verifies the tool.Tool method and
// the directory-less builder agree, so the Agent's default self-description
// (Schema) and its zero-directory form (SchemaWithAgentDirectory) can't drift.
func TestAgentToolSchemaMatchesEmptyDirectory(t *testing.T) {
	at := &AgentTool{}
	if at.Schema().Description != agentSchema("", false).Description {
		t.Error("AgentTool.Schema must equal the directory-less agentSchema")
	}
	if at.SchemaWithAgentDirectory("").Description != agentSchema("", false).Description {
		t.Error("SchemaWithAgentDirectory(\"\") must equal the directory-less agentSchema")
	}
}

func TestAgentSchemaEncouragesDirectWorkForClearScope(t *testing.T) {
	description := agentSchema("", false).Description
	for _, want := range []string{
		"separate context or parallel execution materially helps",
		"Handle clear, bounded work directly",
		"multiple tool calls",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("Agent description should contain %q", want)
		}
	}
}

func TestAgentSchemaRetainsDelegationGuidance(t *testing.T) {
	description := agentSchema("", false).Description
	for _, want := range []string{
		"all context it needs",
		"Use explore for read-only investigation and edit for file changes",
		"Launch independent agents concurrently",
		"Use background mode only for work that does not block your next step",
		"Verify the result before reporting it",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("Agent description should retain %q guidance", want)
		}
	}
}

func TestAgentSchemaBatchFieldsOmittedWhenDisabled(t *testing.T) {
	schema := agentSchema("", false)
	params := schema.Definition.(map[string]any)
	props := params["properties"].(map[string]any)
	if _, ok := props["tasks"]; ok {
		t.Error("tasks[] must not appear in schema when batch is disabled")
	}
	if _, ok := props["context"]; ok {
		t.Error("context must not appear in schema when batch is disabled")
	}
}

func TestAgentSchemaBatchFieldsPresentWhenEnabled(t *testing.T) {
	schema := agentSchema("", true)
	params := schema.Definition.(map[string]any)
	props := params["properties"].(map[string]any)
	if _, ok := props["tasks"]; !ok {
		t.Error("tasks[] must appear in schema when batch is enabled")
	}
	if _, ok := props["context"]; !ok {
		t.Error("context must appear in schema when batch is enabled")
	}
	if !strings.Contains(schema.Description, "tasks[]") {
		t.Error("batch guidance should appear in description when enabled")
	}
}

func TestAgentSchemaExplainsNameResolution(t *testing.T) {
	params := buildAgentToolParameters(false)
	properties := params["properties"].(map[string]any)
	name, ok := properties["name"].(map[string]any)
	if !ok {
		t.Fatal("Agent schema should expose name")
	}
	want := "Choose an available agent, or name a new general-purpose agent for this task. New names are for display only."
	if description := name["description"]; description != want {
		t.Fatalf("name description = %q, want %q", description, want)
	}
}

func TestAgentSchemaModeEnumExcludesBypass(t *testing.T) {
	params := buildAgentToolParameters(false)
	properties := params["properties"].(map[string]any)
	mode := properties["mode"].(map[string]any)
	enum := mode["enum"].([]string)
	want := []string{"explore", "edit", "default"}
	if strings.Join(enum, ",") != strings.Join(want, ",") {
		t.Fatalf("mode enum = %v, want %v", enum, want)
	}
}

func TestAgentSchemaOmitsModelOverride(t *testing.T) {
	params := buildAgentToolParameters(false)
	properties := params["properties"].(map[string]any)
	if _, ok := properties["model"]; ok {
		t.Fatal("Agent schema should not expose a model override")
	}
}

func TestAgentStopSchemaRequiresOnlyTaskID(t *testing.T) {
	params := (&AgentStopTool{}).Schema().Definition.(map[string]any)
	required := params["required"].([]string)
	if len(required) != 1 || required[0] != "task_id" {
		t.Fatalf("AgentStop required fields = %#v, want [task_id]", required)
	}
}
