package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadResolvesPaths(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "sub", "dir")
	repo := filepath.Join(project, "repos", "backend")
	worktrees := filepath.Join(home, "mwt-worktrees")

	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(project, "mwt.yaml")
	if err := os.WriteFile(configPath, []byte(`
worktree_root: ~/mwt-worktrees
base_branch: trunk
repos:
  backend:
    path: ./repos/backend
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(".", "")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.WorktreeRoot != worktrees {
		t.Fatalf("WorktreeRoot = %q, want %q", cfg.WorktreeRoot, worktrees)
	}
	if got := cfg.Repos["backend"].Path; got != repo {
		t.Fatalf("repo path = %q, want %q", got, repo)
	}
	if got := cfg.Repos["backend"].BaseBranch; got != "trunk" {
		t.Fatalf("repo base branch = %q, want trunk", got)
	}
}
