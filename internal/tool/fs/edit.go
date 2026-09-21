package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
	"github.com/genai-io/san/internal/tool/toolresult"
)

const IconEdit = "✏️"

// maxStoredDiffLines caps the unified diff persisted with a result so a huge
// rewrite doesn't bloat the session transcript; the UI shows the cap notice.
const maxStoredDiffLines = 400

// EditTool edits a file using hashline anchors (LINE#hash) and a whole-file
// tag (@file path#TAG). Both must match the on-disk content; stale anchors are
// rejected without any silent partial application.
//
// Workflow:
//  1. Read the file to obtain the @file path#TAG header and LINE#hash|content lines.
//  2. Call Edit with file_tag from the header and one or more edits
//     that reference the LINE#hash anchors.
//  3. After a successful Edit the snapshot is invalidated — re-read before
//     the next Edit on the same file.
type EditTool struct{}

func (t *EditTool) Name() string        { return "Edit" }
func (t *EditTool) Description() string { return "Edit a file using hashline anchors" }
func (t *EditTool) Icon() string        { return IconEdit }

func (t *EditTool) RequiresPermission() bool { return true }

// PreparePermission reads the file, applies the edits as a dry-run, and
// generates a diff for the user approval dialog.
func (t *EditTool) PreparePermission(ctx context.Context, params map[string]any, cwd string) (*perm.PermissionRequest, error) {
	filePath, fileTag, edits, err := parseEditParams(params)
	if err != nil {
		return nil, &tool.ToolError{Message: err.Error()}
	}
	filePath = resolveEditPath(filePath, cwd)

	content, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &tool.ToolError{Message: "file not found: " + filePath}
		}
		return nil, &tool.ToolError{Message: "failed to read file: " + err.Error()}
	}
	if _, err := requireObservedView(filePath); err != nil {
		return nil, &tool.ToolError{Message: err.Error()}
	}

	oldContent, bom, hasWindowsLE := prepareEditContent(string(content))
	newContent, err := ApplyHashlineEdits(oldContent, fileTag, edits)
	if err != nil {
		return nil, &tool.ToolError{Message: editErrMsg(err)}
	}
	if hasWindowsLE {
		newContent = strings.ReplaceAll(newContent, "\n", "\r\n")
	}
	newContent = bom + newContent

	return &perm.PermissionRequest{
		ID:          tool.GenerateRequestID(),
		ToolName:    t.Name(),
		FilePath:    filePath,
		Description: fmt.Sprintf("Edit file (%d operation(s))", len(edits)),
		DiffMeta:    perm.GenerateDiff(filePath, string(content), newContent),
	}, nil
}

// ExecuteApproved applies the hashline edits after user approval.
func (t *EditTool) ExecuteApproved(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	start := time.Now()

	filePath, fileTag, edits, err := parseEditParams(params)
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}
	filePath = resolveEditPath(filePath, cwd)

	content, err := os.ReadFile(filePath)
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), "failed to read file: "+err.Error())
	}
	view, err := requireObservedView(filePath)
	if err != nil {
		return toolresult.NewErrorResult(t.Name(), err.Error())
	}

	oldContent, bom, hasWindowsLE := prepareEditContent(string(content))

	newContent, applyErr := ApplyHashlineEdits(oldContent, fileTag, edits)
	if applyErr != nil {
		msg := editErrMsg(applyErr)
		if view == viewStale {
			msg = filePath + " changed on disk since last read; " + msg
		}
		return toolresult.NewErrorResult(t.Name(), msg)
	}

	if hasWindowsLE {
		newContent = strings.ReplaceAll(newContent, "\n", "\r\n")
	}
	newContent = bom + newContent

	mode := os.FileMode(0o644)
	if info, err := os.Stat(filePath); err == nil {
		mode = info.Mode()
	}
	if err := os.WriteFile(filePath, []byte(newContent), mode); err != nil {
		return toolresult.NewErrorResult(t.Name(), "failed to write file: "+err.Error())
	}
	recordFileWritten(filePath)

	changes := perm.GenerateDiff(filePath, oldContent, newContent)
	storedDiff, truncatedDiffLines := perm.CapUnifiedDiff(changes.UnifiedDiff, maxStoredDiffLines)

	output := fmt.Sprintf(
		"Edited %s (%d edit(s), +%d -%d); snapshot invalidated — re-read before next Edit",
		filePath, len(edits), changes.AddedCount, changes.RemovedCount,
	)

	return toolresult.ToolResult{
		Success: true,
		Output:  output,
		Details: toolresult.FileChangeDetails{
			Path:               filePath,
			EditCount:          len(edits),
			AddedLines:         changes.AddedCount,
			RemovedLines:       changes.RemovedCount,
			UnifiedDiff:        storedDiff,
			TruncatedDiffLines: truncatedDiffLines,
		},
		HookResponse: map[string]any{
			"filePath":     filePath,
			"fileTag":      fileTag,
			"editCount":    len(edits),
			"originalFile": oldContent,
		},
		Metadata: toolresult.ResultMetadata{
			Title:    t.Name(),
			Icon:     t.Icon(),
			Subtitle: filePath,
			Duration: time.Since(start),
		},
	}
}

// Execute is required by the Tool interface; Edit is gated so in practice
// ExecuteApproved is called. This fallback runs it ungated (e.g. in tests).
func (t *EditTool) Execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	return t.ExecuteApproved(ctx, params, cwd)
}

// Schema returns the model-facing tool definition for Edit.
func (t *EditTool) Schema() core.ToolSchema {
	return editSchema()
}

// --- helpers ----------------------------------------------------------------

// parseEditParams extracts and validates Edit call parameters.
func parseEditParams(params map[string]any) (filePath, fileTag string, edits []HashlineEdit, err error) {
	filePath, err = tool.RequireString(params, "file_path")
	if err != nil {
		return
	}
	fileTag, err = tool.RequireString(params, "file_tag")
	if err != nil {
		return
	}

	// edits may arrive as []any (from JSON decode) or []HashlineEdit.
	rawEdits, ok := params["edits"]
	if !ok {
		err = fmt.Errorf("edits is required")
		return
	}

	// Re-encode/decode via JSON to handle []any → []HashlineEdit safely.
	b, jsonErr := json.Marshal(rawEdits)
	if jsonErr != nil {
		err = fmt.Errorf("edits: %w", jsonErr)
		return
	}
	if jsonErr := json.Unmarshal(b, &edits); jsonErr != nil {
		err = fmt.Errorf("edits: %w", jsonErr)
		return
	}
	if len(edits) == 0 {
		err = fmt.Errorf("edits must be a non-empty array")
		return
	}
	return
}

// prepareEditContent strips BOM and normalises line endings for hashing
// and editing. Returns the clean LF-only content, any BOM that was stripped
// (to re-prepend after writing), and whether the original had Windows line
// endings (so the caller can restore them after edit).
func prepareEditContent(content string) (lf, bom string, hadWindowsLE bool) {
	if after, ok := strings.CutPrefix(content, "\ufeff"); ok {
		bom, lf = "\ufeff", after
	} else {
		lf = content
	}
	hadWindowsLE = strings.Contains(lf, "\r\n")
	if hadWindowsLE {
		lf = strings.ReplaceAll(lf, "\r\n", "\n")
	}
	return lf, bom, hadWindowsLE
}

// editErrMsg converts apply errors to short, model-actionable messages.
func editErrMsg(err error) string {
	switch e := err.(type) {
	case *ErrFileTagMismatch:
		return fmt.Sprintf(
			"file tag stale (want %s, current file is %s) — re-read before editing",
			e.Expected, e.Actual,
		)
	case *HashlineMismatchError:
		var b strings.Builder
		b.WriteString("line hash stale — re-read before editing:")
		for _, m := range e.Mismatches {
			fmt.Fprintf(&b, " line %d (want %s got %s);", m.Line, m.Expected, m.Actual)
		}
		return strings.TrimSuffix(b.String(), ";")
	default:
		return err.Error()
	}
}

func resolveEditPath(filePath, cwd string) string {
	if filePath == "" || filePath[0] == '/' {
		return filePath
	}
	if cwd == "" {
		return filePath
	}
	return cwd + "/" + filePath
}

func init() {
	tool.Register(&EditTool{})
}
