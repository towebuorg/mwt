package mwt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guillermo/mwt/internal/config"
	"github.com/guillermo/mwt/internal/git"
)

func TestPlanCreateDetectsExistingBranch(t *testing.T) {
	env := setupIntegrationEnv(t, []string{"backend"})
	planner := env.planner()

	plan, err := planner.PlanCreate(context.Background(), "feature-x")
	if err != nil {
		t.Fatalf("PlanCreate returned error: %v", err)
	}
	if len(plan.Repos) != 1 {
		t.Fatalf("repos = %d, want 1", len(plan.Repos))
	}
	if plan.Repos[0].BranchExists {
		t.Fatal("BranchExists = true, want false")
	}

	created, err := planner.ExecuteCreate(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteCreate returned error: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %d, want 1", len(created))
	}

	plan, err = planner.PlanCreate(context.Background(), "feature-y")
	if err != nil {
		t.Fatalf("PlanCreate second branch returned error: %v", err)
	}
	if plan.Repos[0].BranchExists {
		t.Fatal("BranchExists = true, want false")
	}
}

func TestPlanMergeAndSnapshot(t *testing.T) {
	env := setupIntegrationEnv(t, []string{"backend", "frontend", "infra"})
	planner := env.planner()
	ctx := context.Background()

	plan, err := planner.PlanCreate(ctx, "feature-auth")
	if err != nil {
		t.Fatalf("PlanCreate returned error: %v", err)
	}
	if _, err := planner.ExecuteCreate(ctx, plan); err != nil {
		t.Fatalf("ExecuteCreate returned error: %v", err)
	}

	worktreeFile := filepath.Join(env.worktreeRoot, "feature-auth", "backend", "feature.txt")
	if err := os.WriteFile(worktreeFile, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(worktreeFile), "add", "feature.txt")
	runGit(t, filepath.Dir(worktreeFile), "commit", "-m", "feature work")

	mergePlan, err := planner.PlanMerge(ctx, "feature-auth", false)
	if err != nil {
		t.Fatalf("PlanMerge returned error: %v", err)
	}
	if len(mergePlan.Repos) != 3 {
		t.Fatalf("merge repos = %d, want 3", len(mergePlan.Repos))
	}

	snapshot, err := planner.Snapshot(ctx, "feature-auth")
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if snapshot.Repos["backend"].Branch != "feature-auth" {
		t.Fatalf("snapshot branch = %q, want feature-auth", snapshot.Repos["backend"].Branch)
	}
	if snapshot.Repos["backend"].Commit == "" {
		t.Fatal("snapshot commit is empty")
	}

	if err := planner.ExecuteMerge(ctx, mergePlan); err != nil {
		t.Fatalf("ExecuteMerge returned error: %v", err)
	}
	log := runGitOutput(t, env.repos["backend"], "log", "--oneline", "-1")
	if !strings.Contains(log, "Merge branch 'feature-auth'") {
		t.Fatalf("last commit = %q, want merge commit", log)
	}
}

type integrationEnv struct {
	root         string
	worktreeRoot string
	repos        map[string]string
	cfg          *config.Config
}

func setupIntegrationEnv(t *testing.T, names []string) integrationEnv {
	t.Helper()
	root := t.TempDir()
	reposRoot := filepath.Join(root, "repos")
	worktreeRoot := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(reposRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	repos := make(map[string]string, len(names))
	cfg := &config.Config{
		FilePath:     filepath.Join(root, "mwt.yaml"),
		WorktreeRoot: worktreeRoot,
		BaseBranch:   "main",
		Repos:        map[string]config.Repo{},
	}

	for _, name := range names {
		repoPath := filepath.Join(reposRoot, name)
		if err := os.MkdirAll(repoPath, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, repoPath, "init", "-b", "main")
		runGit(t, repoPath, "config", "user.email", "test@example.com")
		runGit(t, repoPath, "config", "user.name", "Test User")
		if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repoPath, "add", "README.md")
		runGit(t, repoPath, "commit", "-m", "initial")

		repos[name] = repoPath
		cfg.Repos[name] = config.Repo{Path: repoPath, BaseBranch: "main"}
	}

	return integrationEnv{
		root:         root,
		worktreeRoot: worktreeRoot,
		repos:        repos,
		cfg:          cfg,
	}
}

func (e integrationEnv) planner() Planner {
	return Planner{
		Config: e.cfg,
		Git:    git.Runner{},
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}
