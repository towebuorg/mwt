package mwt

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/guillermo/mwt/internal/config"
	"github.com/guillermo/mwt/internal/git"
)

type DiscoveredRepo struct {
	Name    string
	Path    string
	Branch  string
	Remotes map[string]string
}

type SyncReport struct {
	Config  *config.Config
	Added   []string
	Removed []string
	Updated []string
}

func InitWorkspace(ctx context.Context, runner git.Runner, root, configPath string) (*config.Config, error) {
	cfg, report, err := buildWorkspaceConfig(ctx, runner, root, configPath, nil)
	if err != nil {
		return nil, err
	}
	if len(report.Updated) > 0 || len(report.Removed) > 0 {
		return nil, fmt.Errorf("unexpected init state while building config")
	}
	return cfg, nil
}

func SyncWorkspace(ctx context.Context, runner git.Runner, root string, existing *config.Config) (*SyncReport, error) {
	cfg, report, err := buildWorkspaceConfig(ctx, runner, root, existing.FilePath, existing)
	if err != nil {
		return nil, err
	}
	report.Config = cfg
	return report, nil
}

func buildWorkspaceConfig(ctx context.Context, runner git.Runner, root, configPath string, existing *config.Config) (*config.Config, *SyncReport, error) {
	repos, err := DiscoverRepos(ctx, runner, root)
	if err != nil {
		return nil, nil, err
	}
	cfg := &config.Config{
		FilePath:     configPath,
		WorktreeRoot: filepath.Join(root, "worktrees"),
		BaseBranch:   "main",
		Repos:        map[string]config.Repo{},
	}
	report := &SyncReport{}

	if existing != nil {
		cfg.WorktreeRoot = existing.WorktreeRoot
		cfg.BaseBranch = existing.BaseBranch
	}
	if cfg.FilePath == "" {
		defaultPath, err := config.DefaultPath(root)
		if err != nil {
			return nil, nil, err
		}
		cfg.FilePath = defaultPath
	}

	if len(repos) > 0 && (cfg.BaseBranch == "" || cfg.BaseBranch == "main") {
		allSame := true
		first := repos[0].Branch
		for _, repo := range repos[1:] {
			if repo.Branch != first {
				allSame = false
				break
			}
		}
		if allSame && first != "" && first != "HEAD" {
			cfg.BaseBranch = first
		}
	}

	existingNames := map[string]struct{}{}
	if existing != nil {
		for name := range existing.Repos {
			existingNames[name] = struct{}{}
		}
	}

	for _, repo := range repos {
		entry := config.Repo{
			Path:    repo.Path,
			Branch:  repo.Branch,
			Remotes: repo.Remotes,
		}
		if existing != nil {
			if prev, ok := existing.Repos[repo.Name]; ok {
				entry.BaseBranch = prev.BaseBranch
			}
		}
		if entry.BaseBranch == "" {
			if repo.Branch != "" && repo.Branch != "HEAD" {
				entry.BaseBranch = repo.Branch
			} else {
				entry.BaseBranch = cfg.BaseBranch
			}
		}
		cfg.Repos[repo.Name] = entry

		if _, ok := existingNames[repo.Name]; ok {
			report.Updated = append(report.Updated, repo.Name)
			delete(existingNames, repo.Name)
		} else {
			report.Added = append(report.Added, repo.Name)
		}
	}

	for name := range existingNames {
		report.Removed = append(report.Removed, name)
	}
	sort.Strings(report.Added)
	sort.Strings(report.Removed)
	sort.Strings(report.Updated)

	if err := cfg.Save(cfg.FilePath); err != nil {
		return nil, nil, err
	}
	if err := cfg.NormalizeAfterSave(); err != nil {
		return nil, nil, err
	}

	return cfg, report, nil
}

func DiscoverRepos(ctx context.Context, runner git.Runner, root string) ([]DiscoveredRepo, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	var repos []DiscoveredRepo
	seen := map[string]string{}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root {
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") && base != ".git" {
				return filepath.SkipDir
			}
		}

		gitDir := filepath.Join(path, ".git")
		info, statErr := os.Stat(gitDir)
		if statErr == nil && info.IsDir() {
			name := filepath.Base(path)
			if other, ok := seen[name]; ok {
				return fmt.Errorf("duplicate repo name %q for %s and %s", name, other, path)
			}
			branch, err := currentBranch(ctx, runner, path)
			if err != nil {
				return fmt.Errorf("%s: read branch: %w", path, err)
			}
			remotes, err := repoRemotes(ctx, runner, path)
			if err != nil {
				return fmt.Errorf("%s: read remotes: %w", path, err)
			}
			repos = append(repos, DiscoveredRepo{
				Name:    name,
				Path:    path,
				Branch:  branch,
				Remotes: remotes,
			})
			seen[name] = path
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(repos, func(i, j int) bool {
		return repos[i].Name < repos[j].Name
	})
	return repos, nil
}

func repoRemotes(ctx context.Context, runner git.Runner, repo string) (map[string]string, error) {
	output, err := runner.Output(ctx, repo, "remote")
	if err != nil {
		return nil, err
	}
	remotes := map[string]string{}
	for _, remote := range strings.Fields(output) {
		url, err := runner.Output(ctx, repo, "remote", "get-url", remote)
		if err != nil {
			return nil, err
		}
		remotes[remote] = url
	}
	return remotes, nil
}
