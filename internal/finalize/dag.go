package finalize

import (
	"fmt"
	"strings"
	"time"

	"github.com/genai-io/san/internal/tool/batch"
)

// verificationTimeout is the per-command timeout for verification commands.
// Longer than the batch default (120s) to accommodate slow test suites.
const verificationTimeout = 5 * time.Minute

// formatCheckTimeout is the per-command timeout for fast formatting checks.
const formatCheckTimeout = 30 * time.Second

// CommandIDs are the stable IDs used in the verification DAG.
// Using constants prevents typos in depends_on references.
const (
	cmdIDFormat = "format"
	cmdIDTest   = "test"
	cmdIDBuild  = "build"
	cmdIDVet    = "vet"
)

// BuildDAG returns the []batch.BatchCommand representing the verification graph
// for the given tier and package scope.
//
// The DAG structure for STANDARD / FULL is:
//
//	format (gofmt -l)
//	      ↓
//	┌─────────────────────┐
//	test(pkgs)  vet(pkgs)  build(./...)
//
// - format has no deps (cheapest gate; fast-fail for formatting issues).
// - test and vet both depend on format (run after formatting gate, concurrently).
// - build depends on format (compile check after format gate, concurrently with test+vet).
//
// For FAST tier: only format + a per-package compile check.
//
// Resource scheduling note: reviewer API request + go test is excellent
// parallelism (different resources: network/model vs CPU/disk). Multiple
// CPU-heavy commands running simultaneously on a weak machine may slow each
// other down — the MaxConcurrency setting (default 4) is the primary throttle.
// The DAG above naturally limits concurrent heavy commands to 3 (test, vet,
// build), which is reasonable for most developer machines.
//
// Trust: only BuildDAG constructs commands; model-supplied strings never
// flow into the returned BatchCommand slice.
func BuildDAG(tier Tier, packages []string, cwd string) []batch.BatchCommand {
	switch tier {
	case TierFast:
		return buildFastDAG(packages, cwd)
	case TierFull:
		return buildFullDAG(cwd)
	default:
		return buildStandardDAG(packages, cwd)
	}
}

// buildFastDAG builds a FAST-tier DAG: format check only + compile-check on
// each affected package. Used for documentation/comment-only changes.
func buildFastDAG(packages []string, cwd string) []batch.BatchCommand {
	cmds := []batch.BatchCommand{
		{
			ID:      cmdIDFormat,
			Command: "gofmt -l .",
			Timeout: formatCheckTimeout,
		},
	}
	// Add a lightweight compile-check per package (no test execution).
	for i, pkg := range packages {
		id := fmt.Sprintf("build-%d", i)
		cmds = append(cmds, batch.BatchCommand{
			ID:        id,
			Command:   fmt.Sprintf("go build %s", pkg),
			DependsOn: []string{cmdIDFormat},
			Timeout:   verificationTimeout,
		})
	}
	return cmds
}

// buildStandardDAG builds a STANDARD-tier DAG: affected-scope test + vet +
// build. Each test and vet command is scoped to the affected packages, not ./...
func buildStandardDAG(packages []string, cwd string) []batch.BatchCommand {
	cmds := []batch.BatchCommand{
		{
			ID:      cmdIDFormat,
			Command: "gofmt -l .",
			Timeout: formatCheckTimeout,
		},
	}

	if len(packages) == 0 {
		// No Go packages detected — fallback to full build only.
		return append(cmds, batch.BatchCommand{
			ID:        cmdIDBuild,
			Command:   "go build ./...",
			DependsOn: []string{cmdIDFormat},
			Timeout:   verificationTimeout,
		})
	}

	scope := strings.Join(packages, " ")

	cmds = append(cmds,
		batch.BatchCommand{
			ID:        cmdIDTest,
			Command:   fmt.Sprintf("go test %s", scope),
			DependsOn: []string{cmdIDFormat},
			Timeout:   verificationTimeout,
		},
		batch.BatchCommand{
			ID:        cmdIDVet,
			Command:   fmt.Sprintf("go vet %s", scope),
			DependsOn: []string{cmdIDFormat},
			Timeout:   verificationTimeout,
		},
		batch.BatchCommand{
			ID:        cmdIDBuild,
			Command:   "go build ./...",
			DependsOn: []string{cmdIDFormat},
			Timeout:   verificationTimeout,
		},
	)
	return cmds
}

// buildFullDAG builds a FULL-tier DAG: repository-wide test + vet + build.
func buildFullDAG(cwd string) []batch.BatchCommand {
	return []batch.BatchCommand{
		{
			ID:      cmdIDFormat,
			Command: "gofmt -l .",
			Timeout: formatCheckTimeout,
		},
		{
			ID:        cmdIDTest,
			Command:   "go test ./...",
			DependsOn: []string{cmdIDFormat},
			Timeout:   verificationTimeout,
		},
		{
			ID:        cmdIDVet,
			Command:   "go vet ./...",
			DependsOn: []string{cmdIDFormat},
			Timeout:   verificationTimeout,
		},
		{
			ID:        cmdIDBuild,
			Command:   "go build ./...",
			DependsOn: []string{cmdIDFormat},
			Timeout:   verificationTimeout,
		},
	}
}
