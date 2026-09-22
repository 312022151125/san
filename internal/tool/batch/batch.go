// Package batch provides the Batch tool: a deterministic multi-command
// executor that accepts a commands[] array with optional depends_on edges,
// runs independent commands concurrently, propagates fail-fast skips for
// dependent commands, and returns one compact result to the model — all
// without requiring another model reasoning step between commands.
//
// Design principle (Tura-inspired):
//
//	MODEL decides what commands to run
//	  ↓
//	Batch tool receives the full DAG
//	  ↓
//	  ├─ cmd A (independent)
//	  ├─ cmd B (independent)
//	  └─ cmd C (depends on A)
//	  ↓
//	compact result
//	  ↓
//	MODEL re-enters only at semantic boundary
//
// Batch is distinct from Bash (single command = primitive) in the same way
// Agent tasks[] is distinct from a single Agent call.
//
// Permission model: Batch executes with the same policy as the calling agent.
// Agents without Bash access do not receive the Batch tool in their schema.
// Plan Mode rejects Batch entirely via the PlanModeChecker interface.
package batch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/genai-io/san/internal/proc"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
	"github.com/genai-io/san/internal/tool/toolresult"
)

const (
	// MaxCommands is the upper limit on commands in a single Batch call.
	MaxCommands = 8

	// MaxParallel is the upper limit on concurrently executing commands.
	MaxParallel = 4

	// MaxInlineBytes is the maximum number of bytes inlined into the model
	// result per command. Output above this threshold is stored to a file.
	MaxInlineBytes = 8192

	// LargeOutputThreshold is the byte count above which command output is
	// stored to disk and replaced with an ArtifactRef in the result.
	LargeOutputThreshold = 4096

	// defaultCommandTimeout is the per-command timeout when none is specified.
	defaultCommandTimeout = 120 * time.Second

	// batchTotalTimeout is the outer timeout for the entire batch.
	batchTotalTimeout = 600 * time.Second
)

// ArtifactRef is a thin reference to a stored log file (D4: reuse task output dir).
// Format: "log:<absolute-path>" — callers must treat this as opaque.
type ArtifactRef string

// BatchCommand is a single command in a Batch call.
type BatchCommand struct {
	// ID uniquely identifies this command within the batch. Used in depends_on
	// and in the compact result output.
	ID string

	// Command is the shell expression to run (executed via bash -c).
	Command string

	// DependsOn is the list of command IDs that must complete successfully
	// before this command runs. If any listed dependency failed or was skipped,
	// this command is also skipped.
	DependsOn []string

	// Timeout overrides the per-command timeout. 0 uses defaultCommandTimeout.
	Timeout time.Duration
}

// CommandResult is the result of one command in a Batch execution.
type CommandResult struct {
	ID         string
	ExitCode   int
	Duration   time.Duration
	Output     string      // compacted inline output
	LogRef     ArtifactRef // non-empty when output was stored to file
	Skipped    bool
	SkipReason string
	Err        string // non-exit error (e.g. spawn failure)
}

// BatchResult is the combined result of a Batch execution.
type BatchResult struct {
	Commands []CommandResult
	Duration time.Duration
}

// BatchTool executes a deterministic command DAG in a single tool call.
type BatchTool struct {
	planModeChecker tool.PlanModeChecker
	outputDir       string
}

// NewBatchTool creates a new BatchTool.
func NewBatchTool() *BatchTool {
	return &BatchTool{}
}

func (t *BatchTool) Name() string        { return tool.ToolBatch }
func (t *BatchTool) Description() string { return "Execute a deterministic command graph" }
func (t *BatchTool) Icon() string        { return "$" }

// SetPlanModeChecker wires the Plan Mode gate. When active, Batch is rejected
// entirely — no command classification (D2: conservative policy).
func (t *BatchTool) SetPlanModeChecker(c tool.PlanModeChecker) {
	t.planModeChecker = c
}

// SetOutputDir sets the directory for storing large command output logs (D4).
func (t *BatchTool) SetOutputDir(dir string) {
	t.outputDir = dir
}

// RequiresPermission returns true — Batch requires the same approval as Bash.
func (t *BatchTool) RequiresPermission() bool { return true }

// PreparePermission prepares the permission request for a Batch call.
func (t *BatchTool) PreparePermission(ctx context.Context, params map[string]any, cwd string) (*perm.PermissionRequest, error) {
	cmds, err := parseCommands(params)
	if err != nil {
		return nil, err
	}

	var sb strings.Builder
	for _, c := range cmds {
		sb.WriteString(c.Command)
		sb.WriteString("\n")
	}

	return &perm.PermissionRequest{
		ID:       tool.GenerateRequestID(),
		ToolName: t.Name(),
		BashMeta: &perm.BashMetadata{
			Command:   sb.String(),
			LineCount: len(cmds),
		},
	}, nil
}

// ExecuteApproved executes the batch after user approval.
func (t *BatchTool) ExecuteApproved(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	return t.execute(ctx, params, cwd)
}

// Execute implements the Tool interface.
func (t *BatchTool) Execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	return t.execute(ctx, params, cwd)
}

func (t *BatchTool) execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	start := time.Now()

	// Plan Mode rejection (D2) — no command classification, reject entirely.
	if t.planModeChecker != nil && t.planModeChecker.IsPlanMode() {
		return toolresult.NewErrorResult(t.Name(), "Batch is not available in Plan Mode")
	}

	cmds, err := parseCommands(params)
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}

	// Apply outer batch timeout.
	ctx, cancel := context.WithTimeout(ctx, batchTotalTimeout)
	defer cancel()

	result, err := executeBatch(ctx, cmds, cwd, t.outputDir)
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), fmt.Sprintf("batch execution error: %v", err))
	}

	duration := time.Since(start)
	output := formatBatchResult(result)

	// Determine overall success: the batch "succeeds" at the tool level even
	// when individual commands fail — the model needs to see the results.
	return toolresult.ToolResult{
		Success: true,
		Output:  output,
		Metadata: toolresult.ResultMetadata{
			Title:    t.Name(),
			Icon:     t.Icon(),
			Subtitle: formatSubtitle(result),
			Duration: duration,
		},
	}
}

// parseCommands deserializes the commands[] parameter.
func parseCommands(params map[string]any) ([]BatchCommand, error) {
	rawCmds, _ := params["commands"].([]any)
	if len(rawCmds) == 0 {
		return nil, fmt.Errorf("commands[] must be a non-empty array")
	}
	if len(rawCmds) > MaxCommands {
		return nil, fmt.Errorf("commands[] exceeds maximum of %d commands (got %d)", MaxCommands, len(rawCmds))
	}

	cmds := make([]BatchCommand, 0, len(rawCmds))
	seenIDs := make(map[string]bool, len(rawCmds))

	for i, raw := range rawCmds {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("commands[%d]: must be an object", i)
		}
		id := tool.GetString(m, "id")
		if id == "" {
			return nil, fmt.Errorf("commands[%d]: id is required", i)
		}
		if seenIDs[id] {
			return nil, fmt.Errorf("commands[%d]: duplicate id %q", i, id)
		}
		seenIDs[id] = true

		command := tool.GetString(m, "command")
		if command == "" {
			return nil, fmt.Errorf("commands[%d] (%q): command is required", i, id)
		}

		var deps []string
		if rawDeps, ok := m["depends_on"].([]any); ok {
			for _, d := range rawDeps {
				if s, ok := d.(string); ok && s != "" {
					deps = append(deps, s)
				}
			}
		}

		timeoutMs := tool.GetFloat64(m, "timeout_ms", 0)
		timeout := time.Duration(timeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = defaultCommandTimeout
		}

		cmds = append(cmds, BatchCommand{
			ID:        id,
			Command:   command,
			DependsOn: deps,
			Timeout:   timeout,
		})
	}

	return cmds, nil
}

// executeBatch runs the command DAG and returns combined results.
func executeBatch(ctx context.Context, cmds []BatchCommand, cwd, outputDir string) (*BatchResult, error) {
	start := time.Now()

	// Build waves via topological sort.
	waves, err := buildWaves(cmds)
	if err != nil {
		return nil, err
	}

	// Results indexed by command ID.
	results := make(map[string]*CommandResult, len(cmds))
	for i := range cmds {
		results[cmds[i].ID] = &CommandResult{ID: cmds[i].ID}
	}

	// A map from ID to BatchCommand for quick lookup.
	byID := make(map[string]BatchCommand, len(cmds))
	for _, c := range cmds {
		byID[c.ID] = c
	}

	// Execute each wave. Within a wave, commands run concurrently.
	for _, wave := range waves {
		// Check whether any dep for items in this wave failed — if so, skip.
		// For commands with no deps (wave 0) this is always false.
		var wg sync.WaitGroup
		sem := make(chan struct{}, MaxParallel)

		for _, id := range wave {
			c := byID[id]
			res := results[id]

			// Determine if any dependency failed or was skipped.
			skipReason := ""
			for _, dep := range c.DependsOn {
				depRes, ok := results[dep]
				if !ok {
					continue
				}
				if depRes.Skipped {
					skipReason = fmt.Sprintf("dependency %q was skipped", dep)
					break
				}
				if depRes.ExitCode != 0 || depRes.Err != "" {
					skipReason = fmt.Sprintf("dependency %q failed (exit %d)", dep, depRes.ExitCode)
					break
				}
			}

			if skipReason != "" {
				res.Skipped = true
				res.SkipReason = skipReason
				continue
			}

			wg.Add(1)
			go func(c BatchCommand, res *CommandResult) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				runCommand(ctx, c, cwd, outputDir, res)
			}(c, res)
		}
		wg.Wait()
	}

	// Collect in original order.
	ordered := make([]CommandResult, len(cmds))
	for i, c := range cmds {
		ordered[i] = *results[c.ID]
	}

	return &BatchResult{
		Commands: ordered,
		Duration: time.Since(start),
	}, nil
}

// runCommand executes a single command and populates res.
func runCommand(ctx context.Context, c BatchCommand, cwd, outputDir string, res *CommandResult) {
	cmdCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	start := time.Now()

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", c.Command)
	cmd.Dir = cwd
	proc.DetachSession(cmd)
	cmd.Cancel = func() error {
		_ = proc.TerminateGroup(cmd, syscall.SIGKILL)
		return nil
	}
	cmd.WaitDelay = 5 * time.Second

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	runErr := cmd.Run()
	res.Duration = time.Since(start)

	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	} else if runErr != nil {
		res.ExitCode = 1
		res.Err = runErr.Error()
	}

	raw := out.Bytes()

	// Store to file when output is large (D4: reuse task outputDir).
	if len(raw) > LargeOutputThreshold && outputDir != "" {
		path := filepath.Join(outputDir, "batch-"+c.ID+".log")
		if writeErr := os.WriteFile(path, raw, 0o644); writeErr == nil {
			res.LogRef = ArtifactRef("log:" + path)
			// Keep only the relevant extract inline.
			res.Output = extractRelevant(string(raw), MaxInlineBytes)
			return
		}
	}

	res.Output = extractRelevant(string(raw), MaxInlineBytes)
}

// buildWaves performs a topological sort of commands into execution waves.
// Wave 0 contains commands with no dependencies; wave N contains all commands
// whose dependencies are in waves 0..N-1.
// Returns an error if a cycle is detected.
func buildWaves(cmds []BatchCommand) ([][]string, error) {
	// Build index and validate dep references.
	byID := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		byID[c.ID] = true
	}
	inDegree := make(map[string]int, len(cmds))
	deps := make(map[string][]string, len(cmds))
	for _, c := range cmds {
		inDegree[c.ID] = 0
	}
	for _, c := range cmds {
		for _, dep := range c.DependsOn {
			if !byID[dep] {
				return nil, fmt.Errorf("command %q depends on unknown id %q", c.ID, dep)
			}
			inDegree[c.ID]++
			deps[dep] = append(deps[dep], c.ID)
		}
	}

	var waves [][]string
	for {
		// Collect all nodes with in-degree 0.
		var wave []string
		for _, c := range cmds {
			if inDegree[c.ID] == 0 {
				wave = append(wave, c.ID)
			}
		}
		if len(wave) == 0 {
			break
		}
		waves = append(waves, wave)
		// Remove these nodes and reduce in-degrees.
		for _, id := range wave {
			inDegree[id] = -1 // mark as processed
			for _, next := range deps[id] {
				inDegree[next]--
			}
		}
	}

	// If processed < total, there is a cycle.
	processed := 0
	for _, w := range waves {
		processed += len(w)
	}
	if processed < len(cmds) {
		return nil, fmt.Errorf("commands[] contains a dependency cycle")
	}

	return waves, nil
}

// extractRelevant returns a compacted extract of output.
// For failure output, it keeps lines matching error patterns + the last 5 lines.
// Consecutive identical lines are collapsed. Total is capped at maxBytes.
func extractRelevant(output string, maxBytes int) string {
	if output == "" {
		return ""
	}

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	// Deduplicate consecutive identical lines.
	lines = collapseRuns(lines)

	// If short enough, return as-is.
	if len(output) <= maxBytes {
		return strings.Join(lines, "\n")
	}

	// Extract relevant lines (error patterns) + tail.
	var relevant []string
	seen := make(map[string]bool)
	for _, line := range lines {
		if isErrorLine(line) && !seen[line] {
			relevant = append(relevant, line)
			seen[line] = true
		}
	}

	// Always include the last 5 lines.
	tail := lines
	if len(tail) > 5 {
		tail = tail[len(tail)-5:]
	}
	for _, line := range tail {
		if !seen[line] {
			relevant = append(relevant, line)
			seen[line] = true
		}
	}

	result := strings.Join(relevant, "\n")
	if len(result) > maxBytes {
		result = result[:maxBytes] + "\n...(truncated)"
	}
	return result
}

// collapseRuns merges N consecutive identical lines into "<line> (×N)".
func collapseRuns(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	cur := lines[0]
	count := 1
	for _, line := range lines[1:] {
		if line == cur {
			count++
		} else {
			if count > 1 {
				out = append(out, fmt.Sprintf("%s (×%d)", cur, count))
			} else {
				out = append(out, cur)
			}
			cur = line
			count = 1
		}
	}
	if count > 1 {
		out = append(out, fmt.Sprintf("%s (×%d)", cur, count))
	} else {
		out = append(out, cur)
	}
	return out
}

// isErrorLine reports whether a line likely contains error information.
func isErrorLine(line string) bool {
	lower := strings.ToLower(line)
	for _, pattern := range []string{"error:", "fail", "panic:", ".go:"} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// formatSubtitle builds the compact tool result subtitle.
func formatSubtitle(r *BatchResult) string {
	failed := 0
	skipped := 0
	for _, c := range r.Commands {
		if c.Skipped {
			skipped++
		} else if c.ExitCode != 0 || c.Err != "" {
			failed++
		}
	}
	s := fmt.Sprintf("%d commands, %s", len(r.Commands), toolresult.FormatDuration(r.Duration))
	if failed > 0 {
		s += fmt.Sprintf(", %d failed", failed)
	}
	if skipped > 0 {
		s += fmt.Sprintf(", %d skipped", skipped)
	}
	return s
}

func init() {
	tool.Register(NewBatchTool())
}
