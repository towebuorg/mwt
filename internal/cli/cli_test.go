package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletionCommandGeneratesBash(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := Run([]string{"completion", "bash"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "__mwt_get_completion_results") {
		t.Fatalf("bash completion output did not contain Cobra completion function")
	}
}

func TestRepoNameCompletionUsesConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repos", "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "repos", "frontend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mwt.yaml"), []byte(`
worktree_root: ./worktrees
base_branch: main
repos:
  backend:
    path: ./repos/backend
    base_branch: main
    branch: main
  frontend:
    path: ./repos/frontend
    base_branch: main
    branch: main
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code, err := Run([]string{"__completeNoDesc", "repos", ""}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "backend") {
		t.Fatalf("completion output missing backend: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "frontend") {
		t.Fatalf("completion output missing frontend: %s", stdout.String())
	}
}
