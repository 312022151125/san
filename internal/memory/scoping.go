package memory

import (
	"os"
	"path/filepath"
	"strings"
)

// BankID derives the Hindsight memory bank for the given working directory
// and scope. Scoping is intentionally just two modes:
//
//	global   → one shared bank ("san") across every repository.
//	project  → one bank per repository ("san-{label}"), so unrelated
//	           repositories never pollute each other's memories.
//
// The project label follows OMP's per-project derivation: the primary
// checkout root's lowercased basename (so every linked worktree of one
// repository resolves to the same bank, and path capitalization folds
// away). Outside a git repository the working directory itself is the
// project. A hash of the absolute root is appended so two different
// repositories that share a basename ("api" in two orgs) stay isolated —
// the one thing OMP's basename-only scheme gets wrong for San.
func BankID(cwd, scope string) string {
	if scope != settingScopeProject {
		return "san"
	}
	root := projectRoot(cwd)
	label := strings.ToLower(filepath.Base(root))
	if label == "" || label == "." || label == string(filepath.Separator) {
		label = "project"
	}
	return "san-" + label + "-" + rootHash(root)
}

// settingScopeProject mirrors setting.MemoryScopeProject without importing
// the setting package (this package takes plain strings; the caller
// resolves settings). Kept as an unexported const so a typo in the scope
// comparison is impossible here.
const settingScopeProject = "project"

// projectRoot returns the repository's primary checkout root for dir:
// the closest ancestor containing .git, with `.git`-file (worktree)
// pointers resolved through their gitdir to the main checkout. Falls back
// to dir itself outside a repository.
func projectRoot(dir string) string {
	if dir == "" {
		if abs, err := os.Getwd(); err == nil {
			dir = abs
		}
		return dir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for current := abs; ; {
		gitPath := filepath.Join(current, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			if info.IsDir() {
				return current
			}
			// .git file (linked worktree/submodule): "gitdir: <path>".
			// The main checkout root is the parent of the .git directory
			// the gitdir ultimately points at — resolve the common
			// ".../.git/worktrees/<name>" layout directly; anything else
			// (submodules) keeps the worktree root, which is the best
			// stable answer available without invoking git.
			if target := readGitFile(gitPath); target != "" {
				// Linked worktree: gitdir "…/.git/worktrees/<name>" →
				// main checkout root is the parent of "…/.git".
				if idx := strings.Index(target, "/.git/worktrees/"); idx >= 0 {
					return filepath.Dir(target[:idx+len("/.git")])
				}
				// Rare: gitdir is exactly "…/.git" (plain indirection) →
				// its parent is the checkout root.
				if strings.HasSuffix(target, "/.git") {
					return filepath.Dir(target)
				}
				// Submodule (gitdir under "…/.git/modules/…") is its own
				// repository: keep this directory as the project root.
			}
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Filesystem root reached: no repository found.
			return abs
		}
		current = parent
	}
}

// readGitFile parses the single "gitdir: <path>" line of a .git file,
// resolving a relative target against the file's directory. Returns ""
// when the file is malformed.
func readGitFile(gitFile string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if target == "" {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(gitFile), target)
	}
	return filepath.Clean(target)
}

// rootHash returns a short stable hash of the absolute project root so
// same-named repositories stay distinct banks. FNV-1a over the path —
// cheap, deterministic, no crypto needed (this is a namespace, not a
// security boundary).
func rootHash(root string) string {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(root); i++ {
		h ^= uint64(root[i])
		h *= prime64
	}
	const hexDigits = "0123456789abcdef"
	var buf [8]byte
	for i := 7; i >= 0; i-- {
		buf[i] = hexDigits[h&0xf]
		h >>= 4
	}
	return string(buf[:])
}
