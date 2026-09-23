package finalize

import (
	"testing"
)

// --- ClassifyTier ---

func TestClassifyTier_EmptyFiles_ReturnsFull(t *testing.T) {
	tier := ClassifyTier(nil)
	if tier != TierFull {
		t.Errorf("empty file list should return TierFull (safe default), got %s", tier)
	}
}

func TestClassifyTier_GoModForcesFull(t *testing.T) {
	tier := ClassifyTier([]string{"go.mod"})
	if tier != TierFull {
		t.Errorf("go.mod should force TierFull, got %s", tier)
	}
}

func TestClassifyTier_GoSumForcesFull(t *testing.T) {
	tier := ClassifyTier([]string{"go.sum"})
	if tier != TierFull {
		t.Errorf("go.sum should force TierFull, got %s", tier)
	}
}

func TestClassifyTier_CorePackageForcesFull(t *testing.T) {
	tier := ClassifyTier([]string{"internal/core/types.go"})
	if tier != TierFull {
		t.Errorf("internal/core/ change should force TierFull, got %s", tier)
	}
}

func TestClassifyTier_SessionPackageForcesFull(t *testing.T) {
	tier := ClassifyTier([]string{"internal/session/service.go"})
	if tier != TierFull {
		t.Errorf("internal/session/ change should force TierFull, got %s", tier)
	}
}

func TestClassifyTier_SubagentPackageForcesFull(t *testing.T) {
	tier := ClassifyTier([]string{"internal/subagent/executor.go"})
	if tier != TierFull {
		t.Errorf("internal/subagent/ change should force TierFull, got %s", tier)
	}
}

func TestClassifyTier_LLMPackageForcesFull(t *testing.T) {
	tier := ClassifyTier([]string{"internal/llm/vendor_anthropic.go"})
	if tier != TierFull {
		t.Errorf("internal/llm/ change should force TierFull, got %s", tier)
	}
}

func TestClassifyTier_BatchPackageForcesFull(t *testing.T) {
	tier := ClassifyTier([]string{"internal/tool/batch/batch.go"})
	if tier != TierFull {
		t.Errorf("internal/tool/batch/ change should force TierFull, got %s", tier)
	}
}

func TestClassifyTier_BuildConfigForcesFull(t *testing.T) {
	cases := []string{"Makefile", "Dockerfile", ".goreleaser.yml", ".goreleaser.yaml"}
	for _, f := range cases {
		tier := ClassifyTier([]string{f})
		if tier != TierFull {
			t.Errorf("%s should force TierFull, got %s", f, tier)
		}
	}
}

func TestClassifyTier_InterfaceFileForcesFull(t *testing.T) {
	tier := ClassifyTier([]string{"internal/finalize/foo_interface.go"})
	if tier != TierFull {
		t.Errorf("*_interface.go should force TierFull, got %s", tier)
	}
}

func TestClassifyTier_SharedTypesFileForcesFull(t *testing.T) {
	tier := ClassifyTier([]string{"internal/finalize/types.go"})
	if tier != TierFull {
		t.Errorf("types.go should force TierFull, got %s", tier)
	}
}

func TestClassifyTier_MoreThanTwoPackagesForcesFull(t *testing.T) {
	tier := ClassifyTier([]string{
		"internal/finalize/scope.go",
		"internal/agent/session.go",
		"internal/tool/fs/read.go",
	})
	if tier != TierFull {
		t.Errorf("3+ packages should force TierFull, got %s", tier)
	}
}

func TestClassifyTier_SinglePackageImplementation_ReturnsStandard(t *testing.T) {
	tier := ClassifyTier([]string{"internal/finalize/scope.go"})
	if tier != TierStandard {
		t.Errorf("single-package impl change should return TierStandard, got %s", tier)
	}
}

func TestClassifyTier_TwoPackages_ReturnsStandard(t *testing.T) {
	tier := ClassifyTier([]string{
		"internal/finalize/scope.go",
		"internal/finalize/tier.go",
		"internal/plugin/loader.go",
	})
	if tier != TierStandard {
		t.Errorf("2-package change should return TierStandard, got %s", tier)
	}
}

func TestClassifyTier_DocFilesOnly_ReturnsFast(t *testing.T) {
	tier := ClassifyTier([]string{
		"README.md",
		"docs/index.md",
	})
	if tier != TierFast {
		t.Errorf("doc-only changes should return TierFast, got %s", tier)
	}
}

func TestClassifyTier_DocGoOnly_ReturnsFast(t *testing.T) {
	tier := ClassifyTier([]string{
		"internal/finalize/doc.go",
	})
	if tier != TierFast {
		t.Errorf("doc.go-only change should return TierFast, got %s", tier)
	}
}

// --- IsReviewerNeeded ---

func TestIsReviewerNeeded_AlwaysMode(t *testing.T) {
	if !IsReviewerNeeded([]string{"README.md"}, "always") {
		t.Error("always mode should always need reviewer")
	}
}

func TestIsReviewerNeeded_OffMode(t *testing.T) {
	if IsReviewerNeeded([]string{"internal/finalize/scope.go"}, "off") {
		t.Error("off mode should never need reviewer")
	}
}

func TestIsReviewerNeeded_SmartMode_TrivialChange_Skips(t *testing.T) {
	if IsReviewerNeeded([]string{"README.md"}, "smart") {
		t.Error("smart mode should skip reviewer for doc-only changes")
	}
}

func TestIsReviewerNeeded_SmartMode_TestOnlyChange_Skips(t *testing.T) {
	if IsReviewerNeeded([]string{"internal/finalize/scope_test.go"}, "smart") {
		t.Error("smart mode should skip reviewer for single-package test-only changes")
	}
}

func TestIsReviewerNeeded_SmartMode_ImplChange_Needs(t *testing.T) {
	if !IsReviewerNeeded([]string{"internal/finalize/scope.go"}, "smart") {
		t.Error("smart mode should need reviewer for implementation changes")
	}
}

func TestIsReviewerNeeded_SmartMode_RiskyChange_Needs(t *testing.T) {
	if !IsReviewerNeeded([]string{"internal/core/types.go"}, "smart") {
		t.Error("smart mode should need reviewer for risky changes")
	}
}

// --- Tier.String ---

func TestTierString(t *testing.T) {
	cases := map[Tier]string{
		TierFast:     "fast",
		TierStandard: "standard",
		TierFull:     "full",
	}
	for tier, want := range cases {
		if got := tier.String(); got != want {
			t.Errorf("Tier(%d).String() = %q, want %q", tier, got, want)
		}
	}
}
