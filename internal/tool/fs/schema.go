package fs

import (
	"fmt"

	"github.com/genai-io/san/internal/core"
)

// Schema returns the model-facing tool definition for Read. The limits and
// the truncation marker are formatted from the same constants read.go
// enforces, so the model's instructions can't drift from the behavior.
func (t *ReadTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Read",
		Description: fmt.Sprintf(`Reads a file from the local filesystem.

Output format: first line is "@file rel/path#TAG" (whole-file 4-hex fingerprint), then
lines in "LINE#hash|content" format. Supply file_tag and LINE#hash anchors to Edit.

- Prefer relative paths for files inside the session working directory; absolute for targets outside it
- Reads up to %d lines by default; use offset/limit for large files
- Lines over %d characters end with "%s" and cannot be used as Edit anchors
- The file tag covers the entire file even when offset/limit are used, so anchors are always valid
- Do not re-read a file solely to verify your own Edit/Write — a failed change errors; successful results keep your view current
- Image files are recognized but cannot be displayed yet; ask the user to attach the image to a message instead

Large-file workflow: files over %d lines are automatically returned as a head/tail summary
(first %d lines + last %d lines) when no offset/limit is given. To find a specific symbol,
use Grep first, then Read with offset+limit to get the relevant window.`, maxReadLines, maxLineLength, lineTruncationMarker, largeFileThreshold, summaryHead, summaryTail),
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the file to read. Relative paths are resolved from the current session working directory.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "The line number to start reading from (1-based). Providing offset bypasses large-file summary mode.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "The number of lines to read. Providing limit bypasses large-file summary mode.",
				},
				"summary_only": map[string]any{
					"type":        "boolean",
					"description": "When true, always return the head/tail summary regardless of file size. Useful for quick orientation before using Grep.",
				},
			},
			"required": []string{"file_path"},
		},
	}
}

// editSchema returns the model-facing tool definition for Edit.
func editSchema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Edit",
		Description: `Edits a file using hashline anchors from Read output.

Workflow:
1. Read the file to get the "@file path#TAG" header and "LINE#hash|content" lines.
2. Call Edit with file_tag (the TAG from the header) and an edits array using LINE#hash anchors.
3. After a successful Edit the snapshot is invalidated — re-read before the next Edit on the same file.

Edit operations (each element of edits):
- Replace single line:  {"from":"10#abc","to":"10#abc","content":"new line text"}
- Replace range:        {"from":"10#abc","to":"12#ghi","content":"replacement\nlines"}
- Delete line(s):       {"from":"10#abc","to":"10#abc"} (omit content field)
- Insert before line N: {"from":"N#hash","to":"N#hash","content":"new line\nexisting line N content"}

Safety rules:
- If file_tag does not match the current file, the edit is rejected — re-read and retry.
- If any LINE#hash anchor does not match the current line, the edit is rejected — re-read and retry.
- Never guess or construct a file_tag — always copy it verbatim from Read output.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the file to edit. Relative paths are resolved from the current session working directory.",
				},
				"file_tag": map[string]any{
					"type":        "string",
					"description": "Whole-file fingerprint from the @file path#TAG header returned by Read. Example: \"A1B2\". Stale tags are rejected.",
				},
				"edits": map[string]any{
					"type":        "array",
					"description": "Ordered list of hashline edit operations.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"from": map[string]any{
								"type":        "string",
								"description": "Start anchor: LINE#hash (e.g. \"10#abc\"). Copy from Read output.",
							},
							"to": map[string]any{
								"type":        "string",
								"description": "End anchor: LINE#hash (e.g. \"12#ghi\"). Same as from for single-line edits.",
							},
							"content": map[string]any{
								"type":        "string",
								"description": "Replacement text for the from–to range (inclusive). Omit to delete the range.",
							},
						},
						"required": []string{"from", "to"},
					},
				},
			},
			"required": []string{"file_path", "file_tag", "edits"},
		},
	}
}

// Schema returns the model-facing tool definition for Write.
func (t *WriteTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Write",
		Description: `Writes a file to the local filesystem, overwriting any existing content.

- To overwrite, Read the file first unless successful Write/Edit already observed it this session; re-read after external changes.
- Use Edit for every existing-file change; reserve Write for new files or wholesale regeneration.
- Never create documentation or README files unless explicitly requested.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the file to write. Relative paths are resolved from the current session working directory.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The content to write to the file",
				},
			},
			"required": []string{"file_path", "content"},
		},
	}
}

// Schema returns the model-facing tool definition for Bash.
func (t *BashTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Bash",
		Description: `Executes a bash command and returns its output.

- Commands already run in the session working directory — NEVER prefix with "cd <cwd> &&"; use relative paths inside it. A successful cd updates the session working directory; other shell state (variables, aliases) does not persist between calls.
- Search and discovery run through this tool (rg, find/fd, ls); pipe large output through head/wc. Provably read-only commands run without approval prompts.
- For file contents use the dedicated tools: Read (not cat), Edit (not sed), Write (not echo/redirection).
- No TTY and no stdin — anything awaiting interactive input hangs until timeout. Use non-interactive flags ("git commit -m", "npm init -y", "apt-get -y") or feed input via heredoc.
- Optional timeout in ms (default 120000, max 600000). On Unix, run_in_background detaches the command and its result includes the process-group ID plus exact Bash commands for graceful or forced termination.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The command to execute",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Clear, concise description of what this command does in active voice",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Optional timeout in milliseconds (max 600000)",
				},
				"run_in_background": map[string]any{
					"type":        "boolean",
					"description": "Set to true to run this command in the background. You will be notified when it completes.",
				},
			},
			"required": []string{"command"},
		},
	}
}
