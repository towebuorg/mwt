package mwt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/guillermo/mwt/internal/config"
	"github.com/guillermo/mwt/internal/git"
)

func TestInitWorkspaceAndSync(t *testing.T) {
	root := t.TempDir()
	reposRoot := filepath.Join(root, "repos")
	if err := os.MkdirAll(reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	backend := filepath.Join(reposRoot, "backend")
	frontend := filepath.Join(reposRoot, "frontend")
	initRepoWithCommit(t, backend)
	initRepoWithCommit(t, frontend)

	cfgPath := filepath.Join(root, "mwt.yaml")
	cfg, err := InitWorkspace(context.Background(), git.Runner{}, root, cfgPath)
	if err != nil {
		t.Fatalf("InitWorkspace returned error: %v", err)
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("repos = %d, want 2", len(cfg.Repos))
	}
	if cfg.Repos["backend"].Branch != "main" {
		t.Fatalf("backend branch = %q, want main", cfg.Repos["backend"].Branch)
	}

	if err := os.RemoveAll(frontend); err != nil {
		t.Fatal(err)
	}
	report, err := SyncWorkspace(context.Background(), git.Runner{}, root, cfg)
	if err != nil {
		t.Fatalf("SyncWorkspace returned error: %v", err)
	}
	if len(report.Removed) != 1 || report.Removed[0] != "frontend" {
		t.Fatalf("removed = %v, want [frontend]", report.Removed)
	}
	if _, ok := report.Config.Repos["frontend"]; ok {
		t.Fatal("frontend repo still present after sync")
	}
}

func TestExecuteFetchChecksOutBranch(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "backend")
	initRepoWithCommit(t, repo)
	runGit(t, repo, "checkout", "-b", "feature-shared")
	runGit(t, repo, "checkout", "main")

	cfg := &config.Config{
		FilePath:     filepath.Join(root, "mwt.yaml"),
		WorktreeRoot: filepath.Join(root, "worktrees"),
		BaseBranch:   "main",
		Repos: map[string]config.Repo{
			"backend": {
				Path:       repo,
				BaseBranch: "main",
				Branch:     "main",
			},
		},
	}
	planner := Planner{Config: cfg, Git: git.Runner{}}
	plan, err := planner.PlanFetch(context.Background(), "feature-shared")
	if err != nil {
		t.Fatalf("PlanFetch returned error: %v", err)
	}
	if err := planner.ExecuteFetch(context.Background(), plan, false); err != nil {
		t.Fatalf("ExecuteFetch returned error: %v", err)
	}
	branch := runGitOutput(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if got := trimSpace(branch); got != "feature-shared" {
		t.Fatalf("branch = %q, want feature-shared", got)
	}
	if got := planner.Config.Repos["backend"].Branch; got != "feature-shared" {
		t.Fatalf("recorded branch = %q, want feature-shared", got)
	}
}

func initRepoWithCommit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
