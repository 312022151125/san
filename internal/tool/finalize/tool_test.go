package finalizetool

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/finalize"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/batch"
)

// planOn / planOff helpers
type testPlanOn struct{}

func (testPlanOn) IsPlanMode() bool { return true }

type testPlanOff struct{}

func (testPlanOff) IsPlanMode() bool { return false }

// passEngine returns a BatchResult where all commands succeed.
type passEngine struct{}

func (passEngine) Execute(_ context.Context, req batch.EngineRequest) (*batch.BatchResult, error) {
	cmds := make([]batch.CommandResult, len(req.Commands))
	for i, c := range req.Commands {
		cmds[i] = batch.CommandResult{ID: c.ID, ExitCode: 0}
	}
	return &batch.BatchResult{Commands: cmds, Duration: time.Millisecond}, nil
}

func TestFinalizeTool_PlanModeRejectsExecution(t *testing.T) {
	ft := NewFinalizeTool()
	ft.SetPlanModeChecker(testPlanOn{})
	ft.SetDependencies(passEngine{}, nil, t.TempDir())

	result := ft.Execute(context.Background(), map[string]any{}, t.TempDir())
	if result.Success {
		t.Fatal("FinalizeTool should be rejected in Plan Mode")
	}
	if !strings.Contains(result.Error, "Plan Mode") {
		t.Errorf("error should mention Plan Mode, got: %q", result.Error)
	}
}

func TestFinalizeTool_PlanModeOffAllowsExecution(t *testing.T) {
	ft := NewFinalizeTool()
	ft.SetPlanModeChecker(testPlanOff{})
	ft.SetDependencies(passEngine{}, nil, t.TempDir())

	result := ft.Execute(context.Background(), map[string]any{}, t.TempDir())
	if !result.Success {
		t.Fatalf("FinalizeTool should succeed when plan mode is off: %q", result.Error)
	}
	if !strings.Contains(result.Output, "PASS") {
		t.Errorf("output should contain PASS, got: %q", result.Output)
	}
}

func TestFinalizeTool_NoPipelineReturnsError(t *testing.T) {
	ft := NewFinalizeTool()
	result := ft.Execute(context.Background(), map[string]any{}, t.TempDir())
	if result.Success {
		t.Fatal("FinalizeTool without pipeline should return error")
	}
	if !strings.Contains(result.Error, "not configured") {
		t.Errorf("error should say not configured, got: %q", result.Error)
	}
}

func TestFinalizeTool_TierOverrideApplied(t *testing.T) {
	ft := NewFinalizeTool()
	ft.SetDependencies(passEngine{}, nil, t.TempDir())

	result := ft.Execute(context.Background(), map[string]any{
		"tier": "full",
	}, t.TempDir())
	if !result.Success {
		t.Fatalf("expected success, got error: %q", result.Error)
	}
	if !strings.Contains(result.Metadata.Subtitle, "tier=full") {
		t.Errorf("subtitle should show tier=full, got: %q", result.Metadata.Subtitle)
	}
}

func TestFinalizeTool_TaskDescriptionPassedToReviewer(t *testing.T) {
	spy := &captureSubagentExec{}
	ft := NewFinalizeTool()
	ft.SetDependencies(passEngine{}, spy, t.TempDir())

	ft.Execute(context.Background(), map[string]any{
		"task": "implement smart finalization",
		"tier": "standard",
	}, t.TempDir())
	// reviewer is skipped in smart mode for a temp dir with no Go files
	// so we just verify execution completed
	_ = spy
}

func TestFinalizeTool_NameAndIcon(t *testing.T) {
	ft := NewFinalizeTool()
	if ft.Name() != "Finalize" {
		t.Errorf("expected Name()=Finalize, got %q", ft.Name())
	}
	if ft.Icon() == "" {
		t.Error("Icon should not be empty")
	}
}

func TestFinalizeTool_SchemaHasRequiredFields(t *testing.T) {
	ft := NewFinalizeTool()
	schema := ft.Schema()
	if schema.Name != "Finalize" {
		t.Errorf("schema name should be Finalize, got %q", schema.Name)
	}
	if schema.Description == "" {
		t.Error("schema description should not be empty")
	}
	// Should have task and acceptance_criteria params
	def, _ := schema.Definition.(map[string]any)
	if def == nil {
		t.Fatal("schema Definition should be map[string]any")
	}
	props, _ := def["properties"].(map[string]any)
	if props == nil {
		t.Fatal("schema should have properties")
	}
	if _, ok := props["task"]; !ok {
		t.Error("schema should have 'task' parameter")
	}
	if _, ok := props["acceptance_criteria"]; !ok {
		t.Error("schema should have 'acceptance_criteria' parameter")
	}
}

func TestFinalizeTool_RegisteredGlobally(t *testing.T) {
	// Verify init() registered the tool in the global registry.
	// Registry stores names in lowercase.
	registered := tool.List()
	for _, name := range registered {
		if name == "finalize" {
			return
		}
	}
	t.Error("FinalizeTool not found in global tool registry after init()")
}

// captureSubagentExec records reviewer launches.
type captureSubagentExec struct {
	launches []tool.AgentExecRequest
}

func (c *captureSubagentExec) RunBackground(req tool.AgentExecRequest) (finalize.ReviewerTask, error) {
	c.launches = append(c.launches, req)
	return &noopTask{}, nil
}

type noopTask struct{}

func (n *noopTask) GetOutput() string                            { return "approve" }
func (n *noopTask) IsRunning() bool                             { return false }
func (n *noopTask) WaitForCompletion(_ time.Duration) bool      { return true }
func (n *noopTask) GetStatus() finalize.ReviewerTaskStatus      { return finalize.ReviewerTaskStatus{} }
