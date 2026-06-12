package cli

import (
	"bytes"
	"os"
	"os/exec"
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

func TestCloneProjectInitializesConfiguredRepos(t *testing.T) {
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	if err := os.MkdirAll(remotes, 0o755); err != nil {
		t.Fatal(err)
	}

	backendRemote := createBareRemote(t, root, "backend")
	frontendRemote := createBareRemote(t, root, "frontend")

	project := filepath.Join(root, "project-src")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForCLITest(t, project, "init", "-b", "main")
	runGitForCLITest(t, project, "config", "user.email", "test@example.com")
	runGitForCLITest(t, project, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(project, "mwt.yaml"), []byte(`
worktree_root: ./worktrees
base_branch: main
repos:
  backend:
    path: ./repos/backend
    base_branch: main
    branch: main
    remotes:
      origin: `+backendRemote+`
  frontend:
    path: ./repos/frontend
    base_branch: main
    branch: main
    remotes:
      origin: `+frontendRemote+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForCLITest(t, project, "add", "mwt.yaml")
	runGitForCLITest(t, project, "commit", "-m", "project config")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	destRoot := filepath.Join(root, "dest")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(destRoot); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code, err := Run([]string{"clone", project, "workspace"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}

	backend := filepath.Join(destRoot, "workspace", "repos", "backend")
	frontend := filepath.Join(destRoot, "workspace", "repos", "frontend")
	if _, err := os.Stat(filepath.Join(backend, ".git")); err != nil {
		t.Fatalf("backend was not cloned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(frontend, ".git")); err != nil {
		t.Fatalf("frontend was not cloned: %v", err)
	}
	if got := strings.TrimSpace(runGitOutputForCLITest(t, backend, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Fatalf("backend branch = %q, want main", got)
	}
}

func createBareRemote(t *testing.T, root, name string) string {
	t.Helper()
	source := filepath.Join(root, name+"-source")
	bare := filepath.Join(root, "remotes", name+".git")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForCLITest(t, source, "init", "-b", "main")
	runGitForCLITest(t, source, "config", "user.email", "test@example.com")
	runGitForCLITest(t, source, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForCLITest(t, source, "add", "README.md")
	runGitForCLITest(t, source, "commit", "-m", "initial")
	runGitForCLITest(t, root, "init", "--bare", bare)
	runGitForCLITest(t, source, "remote", "add", "origin", bare)
	runGitForCLITest(t, source, "push", "-u", "origin", "main")
	return bare
}

func runGitForCLITest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func runGitOutputForCLITest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}
