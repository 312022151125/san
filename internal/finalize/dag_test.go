package finalize

import (
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/tool/batch"
)

// --- BuildDAG structure ---

func TestBuildDAG_FULL_HasFourCommands(t *testing.T) {
	cmds := BuildDAG(TierFull, nil, "")
	if len(cmds) != 4 {
		t.Fatalf("FULL DAG should have 4 commands (format+test+vet+build), got %d", len(cmds))
	}
}

func TestBuildDAG_STANDARD_HasFourCommandsWithScope(t *testing.T) {
	cmds := BuildDAG(TierStandard, []string{"./internal/finalize"}, "")
	if len(cmds) != 4 {
		t.Fatalf("STANDARD DAG with packages should have 4 commands, got %d", len(cmds))
	}
}

func TestBuildDAG_FAST_HasFormatAndCompile(t *testing.T) {
	cmds := BuildDAG(TierFast, []string{"./internal/finalize"}, "")
	if len(cmds) < 1 {
		t.Fatal("FAST DAG should have at least format command")
	}
	if cmds[0].ID != cmdIDFormat {
		t.Errorf("first FAST command should be format, got %q", cmds[0].ID)
	}
}

func TestBuildDAG_FormatHasNoDeps(t *testing.T) {
	for _, tier := range []Tier{TierFast, TierStandard, TierFull} {
		cmds := BuildDAG(tier, []string{"./internal/finalize"}, "")
		for _, c := range cmds {
			if c.ID == cmdIDFormat && len(c.DependsOn) != 0 {
				t.Errorf("tier=%s: format should have no deps, got %v", tier, c.DependsOn)
			}
		}
	}
}

func TestBuildDAG_TestVetBuildDependOnFormat(t *testing.T) {
	cmds := BuildDAG(TierFull, nil, "")
	byID := make(map[string]batch.BatchCommand, len(cmds))
	for _, c := range cmds {
		byID[c.ID] = c
	}
	for _, id := range []string{cmdIDTest, cmdIDVet, cmdIDBuild} {
		c, ok := byID[id]
		if !ok {
			t.Fatalf("FULL DAG missing command %q", id)
		}
		if !containsString(c.DependsOn, cmdIDFormat) {
			t.Errorf("command %q should depend on format, got %v", id, c.DependsOn)
		}
	}
}

func TestBuildDAG_TestAndVetAreIndependent(t *testing.T) {
	cmds := BuildDAG(TierFull, nil, "")
	byID := make(map[string]batch.BatchCommand, len(cmds))
	for _, c := range cmds {
		byID[c.ID] = c
	}
	// test should NOT depend on vet, and vet should NOT depend on test
	if containsString(byID[cmdIDTest].DependsOn, cmdIDVet) {
		t.Error("test should not depend on vet")
	}
	if containsString(byID[cmdIDVet].DependsOn, cmdIDTest) {
		t.Error("vet should not depend on test")
	}
}

func TestBuildDAG_STANDARD_ScopeNarrowed(t *testing.T) {
	pkgs := []string{"./internal/finalize"}
	cmds := BuildDAG(TierStandard, pkgs, "")
	byID := make(map[string]batch.BatchCommand)
	for _, c := range cmds {
		byID[c.ID] = c
	}
	// test command should reference the specific package, not ./...
	testCmd, ok := byID[cmdIDTest]
	if !ok {
		t.Fatal("STANDARD DAG missing test command")
	}
	if strings.Contains(testCmd.Command, "./...") {
		t.Errorf("STANDARD test should be scope-narrowed, not ./..., got: %q", testCmd.Command)
	}
	if !strings.Contains(testCmd.Command, "./internal/finalize") {
		t.Errorf("STANDARD test should reference the affected package, got: %q", testCmd.Command)
	}
}

func TestBuildDAG_FULL_UsesWildcard(t *testing.T) {
	cmds := BuildDAG(TierFull, nil, "")
	byID := make(map[string]batch.BatchCommand)
	for _, c := range cmds {
		byID[c.ID] = c
	}
	testCmd := byID[cmdIDTest]
	if !strings.Contains(testCmd.Command, "./...") {
		t.Errorf("FULL test should use ./..., got: %q", testCmd.Command)
	}
}

func TestBuildDAG_STANDARD_NoPackages_FallsBackToBuildOnly(t *testing.T) {
	cmds := BuildDAG(TierStandard, nil, "")
	// When no packages, should still have format + build at minimum
	if len(cmds) < 2 {
		t.Fatalf("STANDARD with no packages should have at least 2 commands, got %d", len(cmds))
	}
	ids := make(map[string]bool)
	for _, c := range cmds {
		ids[c.ID] = true
	}
	if !ids[cmdIDFormat] {
		t.Error("should always have format command")
	}
	if !ids[cmdIDBuild] {
		t.Error("STANDARD with no packages should have build command")
	}
}

func TestBuildDAG_TimeoutsSet(t *testing.T) {
	cmds := BuildDAG(TierFull, nil, "")
	for _, c := range cmds {
		if c.Timeout <= 0 {
			t.Errorf("command %q has zero/negative timeout", c.ID)
		}
		if c.ID == cmdIDFormat && c.Timeout >= verificationTimeout {
			t.Errorf("format should have shorter timeout than verification commands")
		}
	}
}

// --- Aggregate ---

func TestAggregate_AllPass(t *testing.T) {
	br := &batch.BatchResult{
		Commands: []batch.CommandResult{
			{ID: "format", ExitCode: 0, Duration: time.Millisecond * 10},
			{ID: "test", ExitCode: 0, Duration: time.Second},
			{ID: "build", ExitCode: 0, Duration: time.Second},
		},
		Duration: time.Second * 2,
	}
	result := Aggregate(br, ReviewerResult{Skipped: true, SkipReason: "test"})
	if !result.Pass {
		t.Error("all-pass batch should produce Pass=true")
	}
	if result.FailureCount != 0 {
		t.Errorf("expected 0 failures, got %d", result.FailureCount)
	}
	// PASS output should be compact
	out := result.String()
	if !strings.Contains(out, "PASS") {
		t.Errorf("PASS output should contain PASS, got: %q", out)
	}
	if strings.Contains(out, "FAILED") {
		t.Errorf("PASS output should not contain FAILED, got: %q", out)
	}
}

func TestAggregate_OneFailure_ActionableOutput(t *testing.T) {
	br := &batch.BatchResult{
		Commands: []batch.CommandResult{
			{ID: "format", ExitCode: 0},
			{ID: "test", ExitCode: 1, Output: "FAIL\tgithub.com/x\nfoo_test.go:42: expected true"},
			{ID: "build", ExitCode: 0},
		},
		Duration: time.Second,
	}
	result := Aggregate(br, ReviewerResult{Skipped: true})
	if result.Pass {
		t.Error("result with failing test should have Pass=false")
	}
	if result.FailureCount != 1 {
		t.Errorf("expected 1 failure, got %d", result.FailureCount)
	}
	out := result.String()
	if !strings.Contains(out, "FAILED") {
		t.Errorf("FAIL output should contain FAILED, got: %q", out)
	}
	if !strings.Contains(out, "foo_test.go:42") {
		t.Errorf("FAIL output should contain actionable error, got: %q", out)
	}
}

func TestAggregate_SkippedCommandReported(t *testing.T) {
	br := &batch.BatchResult{
		Commands: []batch.CommandResult{
			{ID: "format", ExitCode: 1, Output: "error"},
			{ID: "test", Skipped: true, SkipReason: `dependency "format" failed (exit 1)`},
		},
		Duration: time.Second,
	}
	result := Aggregate(br, ReviewerResult{Skipped: true})
	var formatCheck, testCheck *CheckResult
	for i := range result.Checks {
		if result.Checks[i].ID == "format" {
			formatCheck = &result.Checks[i]
		}
		if result.Checks[i].ID == "test" {
			testCheck = &result.Checks[i]
		}
	}
	if formatCheck == nil || formatCheck.State != StateFailed {
		t.Error("format should be StateFailed")
	}
	if testCheck == nil || testCheck.State != StateSkipped {
		t.Error("test should be StateSkipped when format failed")
	}
}

func TestAggregate_ReviewerRequestChanges_FailsResult(t *testing.T) {
	br := &batch.BatchResult{
		Commands: []batch.CommandResult{
			{ID: "format", ExitCode: 0},
		},
		Duration: time.Second,
	}
	rev := ReviewerResult{Verdict: "request-changes", Content: "issues found"}
	result := Aggregate(br, rev)
	if result.Pass {
		t.Error("reviewer request-changes should cause Pass=false")
	}
}

func TestAggregate_NilBatchResult(t *testing.T) {
	// Should not panic
	result := Aggregate(nil, ReviewerResult{Skipped: true})
	_ = result.String()
}

// --- CheckState completeness ---

func TestCheckStateValues(t *testing.T) {
	states := []CheckState{StatePass, StateFailed, StateSkipped, StateCached, StateNotRun}
	for _, s := range states {
		if string(s) == "" {
			t.Errorf("CheckState has empty string value")
		}
	}
}

// --- helpers ---

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
