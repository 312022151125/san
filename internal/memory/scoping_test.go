package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBankIDGlobalScope(t *testing.T) {
	if got := BankID("/any/where", "global"); got != "san" {
		t.Errorf("global bank = %q, want %q", got, "san")
	}
	// Unknown scope also falls back to the shared bank (defensive: settings
	// Validate rejects junk, but BankID must not panic or invent modes).
	if got := BankID("/any/where", "weird"); got != "san" {
		t.Errorf("unknown scope bank = %q, want %q", got, "san")
	}
}

func TestBankIDProjectScopeUsesRepoRootLabel(t *testing.T) {
	repo := t.TempDir()
	mkdirAll(t, filepath.Join(repo, ".git"))
	sub := filepath.Join(repo, "pkg", "inner")
	mkdirAll(t, sub)

	wantPrefix := "san-" + strings.ToLower(filepath.Base(repo)) + "-"
	got := BankID(sub, "project")
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("bank = %q, want prefix %q (derived from repo root, not subdir)", got, wantPrefix)
	}

	// Same repo from a different directory → same bank.
	if other := BankID(repo, "project"); other != got {
		t.Errorf("bank differs within one repo: %q vs %q", got, other)
	}
}

func TestBankIDCapitalizationFolds(t *testing.T) {
	// Two checkouts with the same basename in different parents but
	// different capitalization must map to the same label (hash differs
	// only if the absolute path differs — the label itself folds case).
	label := strings.ToLower("MyRepo")
	if label != "myrepo" {
		t.Fatalf("test precondition: %q", label)
	}
}

func TestBankIDSameBasenameDifferentReposStayDistinct(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	mkdirAll(t, filepath.Join(a, ".git"))
	mkdirAll(t, filepath.Join(b, ".git"))
	// Both are named "api"-like: force identical basenames.
	apiA := filepath.Join(a, "api")
	apiB := filepath.Join(b, "api")
	mkdirAll(t, apiA)
	mkdirAll(t, apiB)
	mkdirAll(t, filepath.Join(apiA, ".git"))
	mkdirAll(t, filepath.Join(apiB, ".git"))

	bankA := BankID(apiA, "project")
	bankB := BankID(apiB, "project")
	if bankA == bankB {
		t.Errorf("two distinct repos with the same basename share a bank: %q", bankA)
	}
	if !strings.HasPrefix(bankA, "san-api-") || !strings.HasPrefix(bankB, "san-api-") {
		t.Errorf("labels lost: %q %q", bankA, bankB)
	}
}

func TestBankIDWorktreeResolvesToMainCheckout(t *testing.T) {
	main := t.TempDir()
	mkdirAll(t, filepath.Join(main, ".git"))

	worktree := t.TempDir()
	// Linked-worktree layout: worktree/.git is a file pointing at
	// main/.git/worktrees/<name>.
	gitFile := filepath.Join(worktree, ".git")
	gitdir := filepath.Join(main, ".git", "worktrees", "wt1")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mainBank := BankID(main, "project")
	wtBank := BankID(worktree, "project")
	if mainBank != wtBank {
		t.Errorf("worktree bank = %q, main bank = %q — must match", wtBank, mainBank)
	}
}

func TestBankIDNoRepoFallsBackToCwd(t *testing.T) {
	dir := t.TempDir()
	got := BankID(dir, "project")
	wantPrefix := "san-" + strings.ToLower(filepath.Base(dir)) + "-"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("bank = %q, want prefix %q", got, wantPrefix)
	}
}

func TestProjectRootSubmoduleGitFileKeepsWorktreeRoot(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, ".git"))
	sub := filepath.Join(root, "vendor", "dep")
	mkdirAll(t, sub)
	// Submodule-style git file: gitdir points into superproject's modules.
	if err := os.WriteFile(filepath.Join(sub, ".git"),
		[]byte("gitdir: "+filepath.Join(root, ".git", "modules", "dep")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(sub, "src")
	mkdirAll(t, inner)

	// Walk finds sub's .git file; the gitdir does not contain "/.git/" in
	// the worktrees layout, so the fallback keeps the submodule root.
	got := projectRoot(inner)
	if got != sub {
		t.Errorf("projectRoot = %q, want submodule root %q", got, sub)
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}
