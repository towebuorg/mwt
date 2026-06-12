package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/guillermo/mwt/internal/config"
	"github.com/guillermo/mwt/internal/git"
	"github.com/guillermo/mwt/internal/mwt"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type globalFlags struct {
	ConfigPath string
	NoColor    bool
	Verbose    bool
}

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

var buildInfo = BuildInfo{
	Version: "dev",
	Commit:  "none",
	Date:    "unknown",
}

func SetBuildInfo(info BuildInfo) {
	buildInfo = info
}

func Run(args []string, stdout, stderr io.Writer) (int, error) {
	root := newRootCommand(stdout, stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(context.Background()); err != nil {
		return 1, err
	}
	return 0, nil
}

func runVersion(stdout io.Writer) (int, error) {
	fmt.Fprintf(stdout, "mwt %s\ncommit: %s\ndate: %s\n", buildInfo.Version, buildInfo.Commit, buildInfo.Date)
	return 0, nil
}

func loadPlanner(global globalFlags, runner git.Runner) (mwt.Planner, error) {
	cfg, err := config.Load(".", global.ConfigPath)
	if err != nil {
		return mwt.Planner{}, err
	}
	return mwt.Planner{
		Config: cfg,
		Git:    runner,
	}, nil
}

func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	var global globalFlags
	styleFor := func() style { return newStyle(stdout, global.NoColor) }
	runnerFor := func() git.Runner {
		return git.Runner{
			Verbose: global.Verbose,
			Logger: func(format string, args ...any) {
				fmt.Fprintf(stderr, format+"\n", args...)
			},
		}
	}
	plannerFor := func() (mwt.Planner, error) {
		return loadPlanner(global, runnerFor())
	}

	root := &cobra.Command{
		Use:              "mwt",
		Short:            "Coordinate Git worktrees across independent repositories",
		SilenceUsage:     true,
		SilenceErrors:    true,
		TraverseChildren: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().StringVar(&global.ConfigPath, "config", "", "config file path")
	root.PersistentFlags().BoolVar(&global.NoColor, "no-color", false, "disable color output")
	root.PersistentFlags().BoolVar(&global.Verbose, "verbose", false, "print underlying Git commands")
	_ = root.MarkPersistentFlagFilename("config", "yaml", "yml")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := runVersion(cmd.OutOrStdout())
			return err
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Discover Git repositories and write mwt.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := runInit(cmd.Context(), runnerFor(), global, cmd.OutOrStdout())
			return err
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "sync",
		Short: "Refresh configured repositories from the workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := runSync(cmd.Context(), runnerFor(), global, cmd.OutOrStdout())
			return err
		},
	})

	fetchCmd := &cobra.Command{
		Use:               "fetch [BRANCH]",
		Short:             "Fetch all repositories and check out a shared branch",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: branchCompletion(global),
		RunE: func(cmd *cobra.Command, args []string) error {
			yes, _ := cmd.Flags().GetBool("yes")
			_, err := runFetchCobra(cmd.Context(), runnerFor(), global, args, yes, cmd.OutOrStdout())
			return err
		},
	}
	fetchCmd.Flags().Bool("yes", false, "confirm checkout of dirty canonical repositories")
	root.AddCommand(fetchCmd)

	root.AddCommand(&cobra.Command{
		Use:   "clone URL [DIR]",
		Short: "Clone a project repository and initialize its configured repos",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := runClone(cmd.Context(), runnerFor(), global, args, cmd.OutOrStdout())
			return err
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "create NAME",
		Short: "Create one feature worktree per configured repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			planner, err := plannerFor()
			if err != nil {
				return err
			}
			_, err = runCreate(cmd.Context(), planner, args, cmd.OutOrStdout(), stderr, styleFor())
			return err
		},
	})

	root.AddCommand(&cobra.Command{
		Use:               "status NAME",
		Short:             "Show status for a named multi-worktree",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: worktreeNameCompletion(global, runnerFor),
		RunE: func(cmd *cobra.Command, args []string) error {
			planner, err := plannerFor()
			if err != nil {
				return err
			}
			_, err = runStatus(cmd.Context(), planner, args, cmd.OutOrStdout(), styleFor())
			return err
		},
	})

	foreachCmd := &cobra.Command{
		Use:               "foreach NAME -- COMMAND...",
		Short:             "Run a command inside each repo worktree",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: worktreeNameCompletion(global, runnerFor),
		RunE: func(cmd *cobra.Command, args []string) error {
			keepGoing, _ := cmd.Flags().GetBool("keep-going")
			planner, err := plannerFor()
			if err != nil {
				return err
			}
			_, err = runForeachCobra(cmd.Context(), planner, args, keepGoing, cmd.OutOrStdout(), stderr)
			return err
		},
	}
	foreachCmd.Flags().Bool("keep-going", false, "continue after a repository command fails")
	root.AddCommand(foreachCmd)

	mergeCmd := &cobra.Command{
		Use:               "merge NAME",
		Short:             "Merge a named branch into each repo base branch",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: worktreeNameCompletion(global, runnerFor),
		RunE: func(cmd *cobra.Command, args []string) error {
			planner, err := plannerFor()
			if err != nil {
				return err
			}
			allowDirty, _ := cmd.Flags().GetBool("allow-dirty")
			yes, _ := cmd.Flags().GetBool("yes")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			_, err = runMergeCobra(cmd.Context(), planner, args[0], allowDirty, yes, dryRun, cmd.OutOrStdout(), stderr, styleFor())
			return err
		},
	}
	mergeCmd.Flags().Bool("allow-dirty", false, "allow dirty worktrees during preflight")
	mergeCmd.Flags().Bool("yes", false, "skip confirmation")
	mergeCmd.Flags().Bool("dry-run", false, "validate and print without changing anything")
	root.AddCommand(mergeCmd)

	removeCmd := &cobra.Command{
		Use:               "remove NAME",
		Short:             "Remove all worktrees for a named multi-worktree",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: worktreeNameCompletion(global, runnerFor),
		RunE: func(cmd *cobra.Command, args []string) error {
			planner, err := plannerFor()
			if err != nil {
				return err
			}
			force, _ := cmd.Flags().GetBool("force")
			deleteBranches, _ := cmd.Flags().GetBool("delete-branches")
			_, err = runRemoveCobra(cmd.Context(), planner, args[0], force, deleteBranches, cmd.OutOrStdout(), styleFor())
			return err
		},
	}
	removeCmd.Flags().Bool("force", false, "force removal of dirty worktrees")
	removeCmd.Flags().Bool("delete-branches", false, "delete feature branches after removing worktrees")
	root.AddCommand(removeCmd)

	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List existing multi-worktrees",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			planner, err := plannerFor()
			if err != nil {
				return err
			}
			_, err = runList(planner, cmd.OutOrStdout())
			return err
		},
	})

	root.AddCommand(&cobra.Command{
		Use:               "snapshot NAME",
		Short:             "Print a lockfile-style commit snapshot",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: worktreeNameCompletion(global, runnerFor),
		RunE: func(cmd *cobra.Command, args []string) error {
			planner, err := plannerFor()
			if err != nil {
				return err
			}
			_, err = runSnapshot(cmd.Context(), planner, args, cmd.OutOrStdout())
			return err
		},
	})

	root.AddCommand(&cobra.Command{
		Use:               "repos [NAME...]",
		Short:             "List configured repositories",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: repoNameCompletion(global),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := runRepos(global, args, cmd.OutOrStdout())
			return err
		},
	})

	return root
}

func runInit(ctx context.Context, runner git.Runner, global globalFlags, stdout io.Writer) (int, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	configPath := global.ConfigPath
	if configPath == "" {
		configPath, err = config.DefaultPath(cwd)
		if err != nil {
			return 1, err
		}
	}
	cfg, err := mwt.InitWorkspace(ctx, runner, cwd, configPath)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "initialized %s with %d repos\n", cfg.FilePath, len(cfg.Repos))
	for _, name := range sortedRepoNames(cfg.Repos) {
		repo := cfg.Repos[name]
		fmt.Fprintf(stdout, "  %s -> %s (%s)\n", name, repo.Path, repo.Branch)
	}
	return 0, nil
}

func runSync(ctx context.Context, runner git.Runner, global globalFlags, stdout io.Writer) (int, error) {
	cfg, err := config.Load(".", global.ConfigPath)
	if err != nil {
		return 2, err
	}
	root := filepath.Dir(cfg.FilePath)
	report, err := mwt.SyncWorkspace(ctx, runner, root, cfg)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "synced %s\n", report.Config.FilePath)
	fmt.Fprintf(stdout, "  added: %s\n", joinOrNone(report.Added))
	fmt.Fprintf(stdout, "  updated: %s\n", joinOrNone(report.Updated))
	fmt.Fprintf(stdout, "  removed: %s\n", joinOrNone(report.Removed))
	return 0, nil
}

func runFetch(ctx context.Context, runner git.Runner, global globalFlags, args []string, stdout io.Writer) (int, error) {
	branch := ""
	yes := false
	for _, arg := range args {
		if arg == "--yes" {
			yes = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return 2, fmt.Errorf("unknown flag %q", arg)
		}
		if branch != "" {
			return 2, errors.New("usage: mwt fetch [BRANCH] [--yes]")
		}
		branch = arg
	}
	planner, err := loadPlanner(global, runner)
	if err != nil {
		return 2, err
	}
	plan, err := planner.PlanFetch(ctx, branch)
	if err != nil {
		return 1, err
	}
	var dirty []string
	for _, repo := range plan.Repos {
		fmt.Fprintf(stdout, "will fetch and checkout %s in %s (current: %s)\n", plan.Branch, repo.Name, repo.CurrentBranch)
		if repo.Dirty {
			dirty = append(dirty, repo.Name)
		}
	}
	force := false
	if len(dirty) > 0 {
		if !yes {
			ok, err := confirm(stdout, os.Stdin, fmt.Sprintf("dirty repos %s will be force-checked out; continue? [y/N]: ", strings.Join(dirty, ", ")))
			if err != nil {
				return 1, err
			}
			if !ok {
				return 1, errors.New("fetch cancelled")
			}
		}
		force = true
	}
	if err := planner.ExecuteFetch(ctx, plan, force); err != nil {
		return 1, err
	}
	if err := planner.Config.Save(""); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "fetched all repos and checked out %s\n", plan.Branch)
	return 0, nil
}

func runFetchCobra(ctx context.Context, runner git.Runner, global globalFlags, args []string, yes bool, stdout io.Writer) (int, error) {
	branch := ""
	if len(args) > 0 {
		branch = args[0]
	}
	planner, err := loadPlanner(global, runner)
	if err != nil {
		return 2, err
	}
	plan, err := planner.PlanFetch(ctx, branch)
	if err != nil {
		return 1, err
	}
	var dirty []string
	for _, repo := range plan.Repos {
		fmt.Fprintf(stdout, "will fetch and checkout %s in %s (current: %s)\n", plan.Branch, repo.Name, repo.CurrentBranch)
		if repo.Dirty {
			dirty = append(dirty, repo.Name)
		}
	}
	force := false
	if len(dirty) > 0 {
		if !yes {
			ok, err := confirm(stdout, os.Stdin, fmt.Sprintf("dirty repos %s will be force-checked out; continue? [y/N]: ", strings.Join(dirty, ", ")))
			if err != nil {
				return 1, err
			}
			if !ok {
				return 1, errors.New("fetch cancelled")
			}
		}
		force = true
	}
	if err := planner.ExecuteFetch(ctx, plan, force); err != nil {
		return 1, err
	}
	if err := planner.Config.Save(""); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "fetched all repos and checked out %s\n", plan.Branch)
	return 0, nil
}

func runClone(ctx context.Context, runner git.Runner, global globalFlags, args []string, stdout io.Writer) (int, error) {
	if len(args) < 1 || len(args) > 2 {
		return 2, errors.New("usage: mwt clone URL [DIR]")
	}
	url := args[0]
	dir := inferCloneDir(url)
	if len(args) == 2 {
		dir = args[1]
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	projectPath := dir
	if !filepath.IsAbs(projectPath) {
		projectPath = filepath.Join(cwd, projectPath)
	}
	if _, err := os.Stat(projectPath); err == nil {
		return 1, fmt.Errorf("project path already exists: %s", projectPath)
	}

	cloneArgs := []string{"clone", url, projectPath}
	if runner.Verbose && runner.Logger != nil {
		runner.Logger("$ git %s", strings.Join(cloneArgs, " "))
	}
	cmd := exec.CommandContext(ctx, "git", cloneArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return 1, fmt.Errorf("clone project repository failed: %w", err)
	}

	cfg, err := config.Load(projectPath, global.ConfigPath)
	if err != nil {
		return 1, fmt.Errorf("project cloned but config could not be loaded: %w", err)
	}

	for _, name := range sortedRepoNames(cfg.Repos) {
		repo := cfg.Repos[name]
		remoteURL, err := cloneRemoteURL(repo)
		if err != nil {
			return 1, fmt.Errorf("%s: %w", name, err)
		}
		if _, err := os.Stat(repo.Path); err == nil {
			return 1, fmt.Errorf("%s: repo path already exists: %s", name, repo.Path)
		}
		if err := os.MkdirAll(filepath.Dir(repo.Path), 0o755); err != nil {
			return 1, fmt.Errorf("%s: create parent directory: %w", name, err)
		}
		cloneRepoArgs := []string{"clone", remoteURL, repo.Path}
		if runner.Verbose && runner.Logger != nil {
			runner.Logger("$ git %s", strings.Join(cloneRepoArgs, " "))
		}
		repoCmd := exec.CommandContext(ctx, "git", cloneRepoArgs...)
		repoCmd.Stdout = stdout
		repoCmd.Stderr = os.Stderr
		if err := repoCmd.Run(); err != nil {
			return 1, fmt.Errorf("%s: clone configured repository failed: %w", name, err)
		}
		fmt.Fprintf(stdout, "cloned %s -> %s\n", name, repo.Path)
	}

	planner := mwt.Planner{Config: cfg, Git: runner}
	plan, err := planner.PlanFetch(ctx, cfg.BaseBranch)
	if err != nil {
		return 1, err
	}
	if err := planner.ExecuteFetch(ctx, plan, false); err != nil {
		return 1, err
	}
	if err := planner.Config.Save(""); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "cloned project %s -> %s and initialized %d repos on %s\n", url, projectPath, len(cfg.Repos), plan.Branch)
	return 0, nil
}

func runCreate(ctx context.Context, planner mwt.Planner, args []string, stdout, stderr io.Writer, style style) (int, error) {
	if len(args) != 1 {
		return 2, errors.New("usage: mwt create NAME")
	}
	name := args[0]
	plan, err := planner.PlanCreate(ctx, name)
	if err != nil {
		return 1, err
	}
	created, err := planner.ExecuteCreate(ctx, plan)
	if err != nil {
		fmt.Fprintf(stdout, "create failed after creating %d worktree(s):\n", len(created))
		for _, path := range created {
			fmt.Fprintf(stdout, "  %s\n", path)
		}
		if len(created) > 0 {
			fmt.Fprintf(stdout, "recovery: mwt remove %s\n", name)
		}
		return 1, err
	}
	for _, repo := range plan.Repos {
		mode := "existing branch"
		if !repo.BranchExists {
			mode = "new branch"
		}
		fmt.Fprintf(stdout, "%s %s -> %s (%s)\n", style.ok("created"), repo.Name, repo.WorktreePath, mode)
	}
	return 0, nil
}

func runStatus(ctx context.Context, planner mwt.Planner, args []string, stdout io.Writer, style style) (int, error) {
	if len(args) != 1 {
		return 2, errors.New("usage: mwt status NAME")
	}
	statuses, err := planner.Status(ctx, args[0])
	if err != nil {
		return 1, err
	}
	for _, status := range statuses {
		state := style.clean("clean")
		if status.Dirty {
			state = style.warn("dirty")
		}
		fmt.Fprintf(stdout, "%-10s %-12s %-5s +%d/-%d\n", status.Name, status.Branch, state, status.Ahead, status.Behind)
		if status.ShortStatus != "" {
			for _, line := range strings.Split(status.ShortStatus, "\n") {
				fmt.Fprintf(stdout, "  %s\n", line)
			}
		}
	}
	return 0, nil
}

func runForeach(ctx context.Context, planner mwt.Planner, args []string, stdout, stderr io.Writer) (int, error) {
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep == -1 {
		return 2, errors.New("usage: mwt foreach NAME [--keep-going] -- COMMAND...")
	}
	pre := args[:sep]
	command := args[sep+1:]
	if len(command) == 0 {
		return 2, errors.New("usage: mwt foreach NAME [--keep-going] -- COMMAND...")
	}
	name, keepGoing, err := parseForeachArgs(pre)
	if err != nil {
		return 2, err
	}
	if name == "" {
		return 2, errors.New("usage: mwt foreach NAME [--keep-going] -- COMMAND...")
	}
	for _, repo := range planner.Config.Repos {
		_ = repo
	}
	for _, repo := range plannerStatusOrder(planner, name) {
		fmt.Fprintf(stdout, "[%s] %s\n", repo.Name, strings.Join(command, " "))
		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Dir = repo.WorktreePath
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			if keepGoing {
				fmt.Fprintf(stderr, "[%s] failed: %v\n", repo.Name, err)
				continue
			}
			return 1, fmt.Errorf("%s: command failed: %w", repo.Name, err)
		}
	}
	return 0, nil
}

func runForeachCobra(ctx context.Context, planner mwt.Planner, args []string, keepGoing bool, stdout, stderr io.Writer) (int, error) {
	if len(args) < 2 {
		return 2, errors.New("usage: mwt foreach NAME -- COMMAND...")
	}
	name := args[0]
	command := args[1:]
	for _, repo := range plannerStatusOrder(planner, name) {
		fmt.Fprintf(stdout, "[%s] %s\n", repo.Name, strings.Join(command, " "))
		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Dir = repo.WorktreePath
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			if keepGoing {
				fmt.Fprintf(stderr, "[%s] failed: %v\n", repo.Name, err)
				continue
			}
			return 1, fmt.Errorf("%s: command failed: %w", repo.Name, err)
		}
	}
	return 0, nil
}

func runMerge(ctx context.Context, planner mwt.Planner, args []string, stdout, stderr io.Writer, style style) (int, error) {
	name, opts, err := parseMergeArgs(args)
	if err != nil {
		return 2, err
	}
	if name == "" {
		return 2, errors.New("usage: mwt merge NAME [--allow-dirty] [--yes] [--dry-run]")
	}
	plan, err := planner.PlanMerge(ctx, name, opts.allowDirty)
	if err != nil {
		return 1, err
	}
	fmt.Fprintln(stdout, "planned merges:")
	for _, repo := range plan.Repos {
		fmt.Fprintf(stdout, "  %s: %s -> %s\n", repo.Name, repo.FeatureBranch, repo.BaseBranch)
	}
	fmt.Fprintln(stdout, "note: merges across repositories are not atomic")
	if opts.dryRun {
		return 0, nil
	}
	if !opts.yes {
		ok, err := confirm(stdout, os.Stdin, "continue? [y/N]: ")
		if err != nil {
			return 1, err
		}
		if !ok {
			return 1, errors.New("merge cancelled")
		}
	}
	if err := planner.ExecuteMerge(ctx, plan); err != nil {
		fmt.Fprintf(stderr, "recovery: resolve conflicts in the reported repo, or abort with `git -C <repo> merge --abort`\n")
		return 1, err
	}
	for _, repo := range plan.Repos {
		fmt.Fprintf(stdout, "%s merged %s into %s for %s\n", style.ok("ok"), repo.FeatureBranch, repo.BaseBranch, repo.Name)
	}
	return 0, nil
}

func runMergeCobra(ctx context.Context, planner mwt.Planner, name string, allowDirty, yes, dryRun bool, stdout, stderr io.Writer, style style) (int, error) {
	plan, err := planner.PlanMerge(ctx, name, allowDirty)
	if err != nil {
		return 1, err
	}
	fmt.Fprintln(stdout, "planned merges:")
	for _, repo := range plan.Repos {
		fmt.Fprintf(stdout, "  %s: %s -> %s\n", repo.Name, repo.FeatureBranch, repo.BaseBranch)
	}
	fmt.Fprintln(stdout, "note: merges across repositories are not atomic")
	if dryRun {
		return 0, nil
	}
	if !yes {
		ok, err := confirm(stdout, os.Stdin, "continue? [y/N]: ")
		if err != nil {
			return 1, err
		}
		if !ok {
			return 1, errors.New("merge cancelled")
		}
	}
	if err := planner.ExecuteMerge(ctx, plan); err != nil {
		fmt.Fprintf(stderr, "recovery: resolve conflicts in the reported repo, or abort with `git -C <repo> merge --abort`\n")
		return 1, err
	}
	for _, repo := range plan.Repos {
		fmt.Fprintf(stdout, "%s merged %s into %s for %s\n", style.ok("ok"), repo.FeatureBranch, repo.BaseBranch, repo.Name)
	}
	return 0, nil
}

func runRemove(ctx context.Context, planner mwt.Planner, args []string, stdout io.Writer, style style) (int, error) {
	name, opts, err := parseRemoveArgs(args)
	if err != nil {
		return 2, err
	}
	if name == "" {
		return 2, errors.New("usage: mwt remove NAME [--force] [--delete-branches]")
	}
	plan, err := planner.PlanRemove(ctx, name, opts.force, opts.deleteBranches)
	if err != nil {
		return 1, err
	}
	if err := planner.ExecuteRemove(ctx, plan); err != nil {
		return 1, err
	}
	for _, repo := range plan.Repos {
		fmt.Fprintf(stdout, "%s %s\n", style.ok("removed"), repo.WorktreePath)
	}
	return 0, nil
}

func runRemoveCobra(ctx context.Context, planner mwt.Planner, name string, force, deleteBranches bool, stdout io.Writer, style style) (int, error) {
	plan, err := planner.PlanRemove(ctx, name, force, deleteBranches)
	if err != nil {
		return 1, err
	}
	if err := planner.ExecuteRemove(ctx, plan); err != nil {
		return 1, err
	}
	for _, repo := range plan.Repos {
		fmt.Fprintf(stdout, "%s %s\n", style.ok("removed"), repo.WorktreePath)
	}
	return 0, nil
}

func runList(planner mwt.Planner, stdout io.Writer) (int, error) {
	names, err := planner.List()
	if err != nil {
		return 1, err
	}
	for _, name := range names {
		fmt.Fprintln(stdout, name)
	}
	return 0, nil
}

func runSnapshot(ctx context.Context, planner mwt.Planner, args []string, stdout io.Writer) (int, error) {
	if len(args) != 1 {
		return 2, errors.New("usage: mwt snapshot NAME")
	}
	snapshot, err := planner.Snapshot(ctx, args[0])
	if err != nil {
		return 1, err
	}
	data, err := yaml.Marshal(snapshot)
	if err != nil {
		return 1, err
	}
	_, err = stdout.Write(data)
	if err != nil {
		return 1, err
	}
	return 0, nil
}

func runRepos(global globalFlags, args []string, stdout io.Writer) (int, error) {
	cfg, err := config.Load(".", global.ConfigPath)
	if err != nil {
		return 1, err
	}
	requested := map[string]struct{}{}
	for _, name := range args {
		requested[name] = struct{}{}
	}
	names := sortedRepoNames(cfg.Repos)
	for _, name := range names {
		if len(requested) > 0 {
			if _, ok := requested[name]; !ok {
				continue
			}
		}
		repo := cfg.Repos[name]
		fmt.Fprintf(stdout, "%-12s %-12s %s\n", name, repo.Branch, repo.Path)
	}
	for name := range requested {
		if _, ok := cfg.Repos[name]; !ok {
			return 1, fmt.Errorf("repo %q is not configured", name)
		}
	}
	return 0, nil
}

func plannerStatusOrder(planner mwt.Planner, name string) []mwt.RepoPlan {
	return plannerRepoPlans(planner, name)
}

type mergeOptions struct {
	allowDirty bool
	yes        bool
	dryRun     bool
}

func parseMergeArgs(args []string) (string, mergeOptions, error) {
	var opts mergeOptions
	var name string
	for _, arg := range args {
		switch arg {
		case "--allow-dirty":
			opts.allowDirty = true
		case "--yes":
			opts.yes = true
		case "--dry-run":
			opts.dryRun = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", opts, fmt.Errorf("unknown flag %q", arg)
			}
			if name != "" {
				return "", opts, errors.New("merge accepts exactly one NAME")
			}
			name = arg
		}
	}
	return name, opts, nil
}

type removeOptions struct {
	force          bool
	deleteBranches bool
}

func parseRemoveArgs(args []string) (string, removeOptions, error) {
	var opts removeOptions
	var name string
	for _, arg := range args {
		switch arg {
		case "--force":
			opts.force = true
		case "--delete-branches":
			opts.deleteBranches = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", opts, fmt.Errorf("unknown flag %q", arg)
			}
			if name != "" {
				return "", opts, errors.New("remove accepts exactly one NAME")
			}
			name = arg
		}
	}
	return name, opts, nil
}

func parseForeachArgs(args []string) (string, bool, error) {
	var name string
	var keepGoing bool
	for _, arg := range args {
		switch arg {
		case "--keep-going":
			keepGoing = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("unknown flag %q", arg)
			}
			if name != "" {
				return "", false, errors.New("foreach accepts exactly one NAME before --")
			}
			name = arg
		}
	}
	return name, keepGoing, nil
}

func sortedRepoNames(repos map[string]config.Repo) []string {
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

func inferCloneDir(url string) string {
	trimmed := strings.TrimRight(url, "/")
	if i := strings.LastIndexAny(trimmed, "/:"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if trimmed == "" || trimmed == "." {
		return "mwt-project"
	}
	return trimmed
}

func cloneRemoteURL(repo config.Repo) (string, error) {
	if len(repo.Remotes) == 0 {
		return "", errors.New("no remotes configured")
	}
	if origin := repo.Remotes["origin"]; origin != "" {
		return origin, nil
	}
	names := make([]string, 0, len(repo.Remotes))
	for name := range repo.Remotes {
		names = append(names, name)
	}
	sortStrings(names)
	return repo.Remotes[names[0]], nil
}

func repoNameCompletion(global globalFlags) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		cfg, err := config.Load(".", global.ConfigPath)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		used := map[string]struct{}{}
		for _, arg := range args {
			used[arg] = struct{}{}
		}
		var completions []string
		for _, name := range sortedRepoNames(cfg.Repos) {
			if _, ok := used[name]; ok {
				continue
			}
			if !strings.HasPrefix(name, toComplete) {
				continue
			}
			completions = append(completions, fmt.Sprintf("%s\t%s", name, cfg.Repos[name].Path))
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

func worktreeNameCompletion(global globalFlags, runnerFor func() git.Runner) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		planner, err := loadPlanner(global, runnerFor())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names, err := planner.List()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var completions []string
		for _, name := range names {
			if strings.HasPrefix(name, toComplete) {
				completions = append(completions, name)
			}
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

func branchCompletion(global globalFlags) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		cfg, err := config.Load(".", global.ConfigPath)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		branches := map[string]struct{}{}
		runner := git.Runner{}
		for _, repo := range cfg.Repos {
			output, err := runner.Output(cmd.Context(), repo.Path, "branch", "--format", "%(refname:short)")
			if err != nil {
				continue
			}
			for _, branch := range strings.Fields(output) {
				if strings.HasPrefix(branch, toComplete) {
					branches[branch] = struct{}{}
				}
			}
		}
		names := make([]string, 0, len(branches))
		for branch := range branches {
			names = append(names, branch)
		}
		sortStrings(names)
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func plannerRepoPlans(planner mwt.Planner, name string) []mwt.RepoPlan {
	plans := make([]mwt.RepoPlan, 0, len(planner.Config.Repos))
	for repoName, repo := range planner.Config.Repos {
		plans = append(plans, mwt.RepoPlan{
			Name:          repoName,
			RepoPath:      repo.Path,
			WorktreePath:  filepath.Join(planner.Config.WorktreeRoot, name, repoName),
			BaseBranch:    repo.BaseBranch,
			FeatureBranch: name,
		})
	}
	sortRepoPlans(plans)
	return plans
}

func sortRepoPlans(plans []mwt.RepoPlan) {
	for i := 0; i < len(plans); i++ {
		for j := i + 1; j < len(plans); j++ {
			if plans[j].Name < plans[i].Name {
				plans[i], plans[j] = plans[j], plans[i]
			}
		}
	}
}

func sortStrings(values []string) {
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

func confirm(stdout io.Writer, stdin io.Reader, prompt string) (bool, error) {
	fmt.Fprint(stdout, prompt)
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: mwt [--config PATH] [--no-color] [--verbose] <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  version")
	fmt.Fprintln(w, "  init")
	fmt.Fprintln(w, "  sync")
	fmt.Fprintln(w, "  fetch [BRANCH] [--yes]")
	fmt.Fprintln(w, "  clone URL [DIR]")
	fmt.Fprintln(w, "  create NAME")
	fmt.Fprintln(w, "  status NAME")
	fmt.Fprintln(w, "  foreach NAME [--keep-going] -- COMMAND...")
	fmt.Fprintln(w, "  merge NAME [--allow-dirty] [--yes] [--dry-run]")
	fmt.Fprintln(w, "  remove NAME [--force] [--delete-branches]")
	fmt.Fprintln(w, "  list")
	fmt.Fprintln(w, "  snapshot NAME")
}
