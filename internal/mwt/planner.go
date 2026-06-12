package mwt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/guillermo/mwt/internal/config"
	"github.com/guillermo/mwt/internal/git"
)

type Planner struct {
	Config *config.Config
	Git    git.Runner
}

type RepoPlan struct {
	Name          string
	RepoPath      string
	WorktreePath  string
	BaseBranch    string
	FeatureBranch string
}

type CreatePlan struct {
	Name  string
	Repos []CreateRepoPlan
}

type CreateRepoPlan struct {
	RepoPlan
	BranchExists bool
}

type MergePlan struct {
	Name  string
	Repos []RepoPlan
}

type RemovePlan struct {
	Name           string
	DeleteBranches bool
	Force          bool
	Repos          []RepoPlan
}

type RepoStatus struct {
	Name         string
	WorktreePath string
	Branch       string
	Dirty        bool
	Ahead        int
	Behind       int
	ShortStatus  string
}

type Snapshot struct {
	Name  string         `yaml:"name"`
	Repos map[string]Ref `yaml:"repos"`
}

type Ref struct {
	Branch string `yaml:"branch"`
	Commit string `yaml:"commit"`
}

type FetchPlan struct {
	Branch string
	Repos  []FetchRepoPlan
}

type FetchRepoPlan struct {
	Name          string
	RepoPath      string
	CurrentBranch string
	Dirty         bool
}

func (p Planner) repoPlans(name string) []RepoPlan {
	names := make([]string, 0, len(p.Config.Repos))
	for repoName := range p.Config.Repos {
		names = append(names, repoName)
	}
	sort.Strings(names)

	plans := make([]RepoPlan, 0, len(names))
	for _, repoName := range names {
		repo := p.Config.Repos[repoName]
		plans = append(plans, RepoPlan{
			Name:          repoName,
			RepoPath:      repo.Path,
			WorktreePath:  filepath.Join(p.Config.WorktreeRoot, name, repoName),
			BaseBranch:    repo.BaseBranch,
			FeatureBranch: name,
		})
	}
	return plans
}

func (p Planner) PlanCreate(ctx context.Context, name string) (*CreatePlan, error) {
	plan := &CreatePlan{Name: name}
	for _, repo := range p.repoPlans(name) {
		if err := ensureGitRepo(repo.RepoPath); err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		if _, err := os.Stat(repo.WorktreePath); err == nil {
			return nil, fmt.Errorf("%s: worktree path already exists: %s", repo.Name, repo.WorktreePath)
		}
		if err := ensureWorktreeParent(repo.WorktreePath); err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		if err := ensureClean(ctx, p.Git, repo.RepoPath); err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		if err := ensureBranchExists(ctx, p.Git, repo.RepoPath, repo.BaseBranch); err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}

		branchExists, err := branchExists(ctx, p.Git, repo.RepoPath, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		if branchExists {
			checkedOut, err := branchCheckedOutElsewhere(ctx, p.Git, repo.RepoPath, name)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", repo.Name, err)
			}
			if checkedOut {
				return nil, fmt.Errorf("%s: branch %q is already checked out in another worktree", repo.Name, name)
			}
		}

		plan.Repos = append(plan.Repos, CreateRepoPlan{
			RepoPlan:     repo,
			BranchExists: branchExists,
		})
	}
	return plan, nil
}

func (p Planner) ExecuteCreate(ctx context.Context, plan *CreatePlan) ([]string, error) {
	created := make([]string, 0, len(plan.Repos))
	for _, repo := range plan.Repos {
		args := []string{"worktree", "add"}
		if !repo.BranchExists {
			args = append(args, "-b", repo.FeatureBranch, repo.WorktreePath, repo.BaseBranch)
		} else {
			args = append(args, repo.WorktreePath, repo.FeatureBranch)
		}
		if err := p.Git.Run(ctx, repo.RepoPath, args...); err != nil {
			return created, err
		}
		created = append(created, repo.WorktreePath)
	}
	return created, nil
}

func (p Planner) Status(ctx context.Context, name string) ([]RepoStatus, error) {
	statuses := make([]RepoStatus, 0, len(p.Config.Repos))
	for _, repo := range p.repoPlans(name) {
		if _, err := os.Stat(repo.WorktreePath); err != nil {
			return nil, fmt.Errorf("%s: worktree not found: %s", repo.Name, repo.WorktreePath)
		}
		branch, err := p.Git.Output(ctx, repo.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		shortStatus, err := p.Git.Output(ctx, repo.WorktreePath, "status", "--short")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		divergence, err := p.Git.Output(ctx, repo.WorktreePath, "rev-list", "--left-right", "--count", fmt.Sprintf("%s...HEAD", repo.BaseBranch))
		if err != nil {
			return nil, fmt.Errorf("%s: compare with %s: %w", repo.Name, repo.BaseBranch, err)
		}
		behind, ahead, err := parseCounts(divergence)
		if err != nil {
			return nil, fmt.Errorf("%s: parse ahead/behind: %w", repo.Name, err)
		}
		statuses = append(statuses, RepoStatus{
			Name:         repo.Name,
			WorktreePath: repo.WorktreePath,
			Branch:       branch,
			Dirty:        shortStatus != "",
			Ahead:        ahead,
			Behind:       behind,
			ShortStatus:  shortStatus,
		})
	}
	return statuses, nil
}

func (p Planner) PlanMerge(ctx context.Context, name string, allowDirty bool) (*MergePlan, error) {
	plan := &MergePlan{Name: name}
	for _, repo := range p.repoPlans(name) {
		if _, err := os.Stat(repo.WorktreePath); err != nil {
			return nil, fmt.Errorf("%s: worktree not found: %s", repo.Name, repo.WorktreePath)
		}
		if err := ensureGitRepo(repo.RepoPath); err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		branch, err := p.Git.Output(ctx, repo.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		if branch != name {
			return nil, fmt.Errorf("%s: worktree is on branch %q, expected %q", repo.Name, branch, name)
		}
		if !allowDirty {
			if err := ensureClean(ctx, p.Git, repo.WorktreePath); err != nil {
				return nil, fmt.Errorf("%s: %w", repo.Name, err)
			}
		}
		if err := ensureBranchExists(ctx, p.Git, repo.RepoPath, repo.BaseBranch); err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		if err := ensureBranchExists(ctx, p.Git, repo.RepoPath, name); err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		plan.Repos = append(plan.Repos, repo)
	}
	return plan, nil
}

func (p Planner) ExecuteMerge(ctx context.Context, plan *MergePlan) error {
	for _, repo := range plan.Repos {
		if err := p.Git.Run(ctx, repo.RepoPath, "checkout", repo.BaseBranch); err != nil {
			return fmt.Errorf("%s: checkout %s failed: %w", repo.Name, repo.BaseBranch, err)
		}
		if err := p.Git.Run(ctx, repo.RepoPath, "merge", "--no-ff", repo.FeatureBranch); err != nil {
			return fmt.Errorf("%s: merge failed: %w", repo.Name, err)
		}
	}
	return nil
}

func (p Planner) PlanRemove(ctx context.Context, name string, force, deleteBranches bool) (*RemovePlan, error) {
	plan := &RemovePlan{Name: name, Force: force, DeleteBranches: deleteBranches}
	for _, repo := range p.repoPlans(name) {
		if _, err := os.Stat(repo.WorktreePath); err != nil {
			return nil, fmt.Errorf("%s: worktree not found: %s", repo.Name, repo.WorktreePath)
		}
		if !force {
			if err := ensureClean(ctx, p.Git, repo.WorktreePath); err != nil {
				return nil, fmt.Errorf("%s: %w", repo.Name, err)
			}
		}
		plan.Repos = append(plan.Repos, repo)
	}
	return plan, nil
}

func (p Planner) ExecuteRemove(ctx context.Context, plan *RemovePlan) error {
	for _, repo := range plan.Repos {
		args := []string{"worktree", "remove"}
		if plan.Force {
			args = append(args, "--force")
		}
		args = append(args, repo.WorktreePath)
		if err := p.Git.Run(ctx, repo.RepoPath, args...); err != nil {
			return fmt.Errorf("%s: remove worktree failed: %w", repo.Name, err)
		}
		if plan.DeleteBranches {
			if err := p.Git.Run(ctx, repo.RepoPath, "branch", "-d", repo.FeatureBranch); err != nil {
				return fmt.Errorf("%s: delete branch failed: %w", repo.Name, err)
			}
		}
	}
	return nil
}

func (p Planner) List() ([]string, error) {
	entries, err := os.ReadDir(p.Config.WorktreeRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read worktree root: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (p Planner) Snapshot(ctx context.Context, name string) (*Snapshot, error) {
	out := &Snapshot{Name: name, Repos: map[string]Ref{}}
	for _, repo := range p.repoPlans(name) {
		if _, err := os.Stat(repo.WorktreePath); err != nil {
			return nil, fmt.Errorf("%s: worktree not found: %s", repo.Name, repo.WorktreePath)
		}
		branch, err := p.Git.Output(ctx, repo.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		commit, err := p.Git.Output(ctx, repo.WorktreePath, "rev-parse", "--short", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo.Name, err)
		}
		out.Repos[repo.Name] = Ref{Branch: branch, Commit: commit}
	}
	return out, nil
}

func (p Planner) PlanFetch(ctx context.Context, branch string) (*FetchPlan, error) {
	if branch == "" {
		branch = p.Config.BaseBranch
	}
	plan := &FetchPlan{Branch: branch}
	for _, repoPlan := range p.repoPlans(branch) {
		if err := ensureGitRepo(repoPlan.RepoPath); err != nil {
			return nil, fmt.Errorf("%s: %w", repoPlan.Name, err)
		}
		currentBranch, err := currentBranch(ctx, p.Git, repoPlan.RepoPath)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repoPlan.Name, err)
		}
		status, err := p.Git.Output(ctx, repoPlan.RepoPath, "status", "--porcelain")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repoPlan.Name, err)
		}
		plan.Repos = append(plan.Repos, FetchRepoPlan{
			Name:          repoPlan.Name,
			RepoPath:      repoPlan.RepoPath,
			CurrentBranch: currentBranch,
			Dirty:         status != "",
		})
	}
	return plan, nil
}

func (p Planner) ExecuteFetch(ctx context.Context, plan *FetchPlan, forceCheckout bool) error {
	for _, repo := range plan.Repos {
		if err := p.Git.Run(ctx, repo.RepoPath, "fetch", "--all", "--prune"); err != nil {
			return fmt.Errorf("%s: fetch failed: %w", repo.Name, err)
		}
		if err := checkoutBranch(ctx, p.Git, repo.RepoPath, plan.Branch, forceCheckout); err != nil {
			return fmt.Errorf("%s: checkout %s failed: %w", repo.Name, plan.Branch, err)
		}
	}
	for name, repo := range p.Config.Repos {
		repo.Branch = plan.Branch
		p.Config.Repos[name] = repo
	}
	return nil
}

func ensureGitRepo(path string) error {
	info, err := os.Stat(filepath.Join(path, ".git"))
	if err == nil && info != nil {
		return nil
	}
	if err == nil {
		return nil
	}
	return fmt.Errorf("not a Git repository: %s", path)
}

func currentBranch(ctx context.Context, runner git.Runner, repo string) (string, error) {
	return runner.Output(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
}

func ensureWorktreeParent(path string) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create parent directory %s: %w", parent, err)
	}
	return nil
}

func ensureClean(ctx context.Context, runner git.Runner, repo string) error {
	status, err := runner.Output(ctx, repo, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("repository has uncommitted changes")
	}
	return nil
}

func ensureBranchExists(ctx context.Context, runner git.Runner, repo, branch string) error {
	ok, err := branchExists(ctx, runner, repo, branch)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("branch %q does not exist", branch)
	}
	return nil
}

func branchExists(ctx context.Context, runner git.Runner, repo, branch string) (bool, error) {
	_, err := runner.Output(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "exit status 1") {
		return false, nil
	}
	return false, err
}

func branchCheckedOutElsewhere(ctx context.Context, runner git.Runner, repo, branch string) (bool, error) {
	out, err := runner.Output(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, block := range strings.Split(out, "\n\n") {
		var head string
		var branchRef string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "branch ") {
				branchRef = strings.TrimPrefix(line, "branch refs/heads/")
			}
			if strings.HasPrefix(line, "worktree ") {
				head = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			}
		}
		if branchRef == branch && head != "" {
			return true, nil
		}
	}
	return false, nil
}

func remoteBranchExists(ctx context.Context, runner git.Runner, repo, remote, branch string) (bool, error) {
	_, err := runner.Output(ctx, repo, "show-ref", "--verify", "--quiet", "refs/remotes/"+remote+"/"+branch)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "exit status 1") {
		return false, nil
	}
	return false, err
}

func checkoutBranch(ctx context.Context, runner git.Runner, repo, branch string, force bool) error {
	if ok, err := branchExists(ctx, runner, repo, branch); err != nil {
		return err
	} else if ok {
		args := []string{"checkout"}
		if force {
			args = append(args, "--force")
		}
		args = append(args, branch)
		return runner.Run(ctx, repo, args...)
	}

	remotesOutput, err := runner.Output(ctx, repo, "remote")
	if err != nil {
		return err
	}
	for _, remote := range strings.Fields(remotesOutput) {
		ok, err := remoteBranchExists(ctx, runner, repo, remote, branch)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if force {
			return runner.Run(ctx, repo, "checkout", "-B", branch, "--track", remote+"/"+branch)
		}
		return runner.Run(ctx, repo, "checkout", "-b", branch, "--track", remote+"/"+branch)
	}

	return fmt.Errorf("branch %q not found locally or on any configured remote", branch)
}

func parseCounts(raw string) (behind, ahead int, err error) {
	parts := strings.Fields(raw)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected count format %q", raw)
	}
	_, err = fmt.Sscanf(parts[0], "%d", &behind)
	if err != nil {
		return 0, 0, err
	}
	_, err = fmt.Sscanf(parts[1], "%d", &ahead)
	if err != nil {
		return 0, 0, err
	}
	return behind, ahead, nil
}
