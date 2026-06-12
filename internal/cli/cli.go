package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/guillermo/mwt/internal/config"
	"github.com/guillermo/mwt/internal/git"
	"github.com/guillermo/mwt/internal/mwt"
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
	ctx := context.Background()
	global, rest, err := parseGlobalFlags(args)
	if err != nil {
		return 2, err
	}
	if len(rest) == 0 {
		printUsage(stdout)
		return 2, errors.New("missing command")
	}
	style := newStyle(stdout, global.NoColor)
	logger := func(format string, args ...any) {
		fmt.Fprintf(stderr, format+"\n", args...)
	}
	runner := git.Runner{
		Verbose: global.Verbose,
		Logger:  logger,
	}

	switch rest[0] {
	case "version":
		return runVersion(stdout)
	case "init":
		return runInit(ctx, runner, global, stdout)
	case "sync":
		return runSync(ctx, runner, global, stdout)
	case "fetch":
		return runFetch(ctx, runner, global, rest[1:], stdout)
	case "clone":
		return runClone(ctx, runner, global, rest[1:], stdout)
	case "create":
		planner, err := loadPlanner(global, runner)
		if err != nil {
			return 2, err
		}
		return runCreate(ctx, planner, rest[1:], stdout, stderr, style)
	case "status":
		planner, err := loadPlanner(global, runner)
		if err != nil {
			return 2, err
		}
		return runStatus(ctx, planner, rest[1:], stdout, style)
	case "foreach":
		planner, err := loadPlanner(global, runner)
		if err != nil {
			return 2, err
		}
		return runForeach(ctx, planner, rest[1:], stdout, stderr)
	case "merge":
		planner, err := loadPlanner(global, runner)
		if err != nil {
			return 2, err
		}
		return runMerge(ctx, planner, rest[1:], stdout, stderr, style)
	case "remove":
		planner, err := loadPlanner(global, runner)
		if err != nil {
			return 2, err
		}
		return runRemove(ctx, planner, rest[1:], stdout, style)
	case "list":
		planner, err := loadPlanner(global, runner)
		if err != nil {
			return 2, err
		}
		return runList(planner, stdout)
	case "snapshot":
		planner, err := loadPlanner(global, runner)
		if err != nil {
			return 2, err
		}
		return runSnapshot(ctx, planner, rest[1:], stdout)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0, nil
	default:
		printUsage(stdout)
		return 2, fmt.Errorf("unknown command %q", rest[0])
	}
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

func parseGlobalFlags(args []string) (globalFlags, []string, error) {
	var global globalFlags
	fs := flag.NewFlagSet("mwt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&global.ConfigPath, "config", "", "config file path")
	fs.BoolVar(&global.NoColor, "no-color", false, "disable color output")
	fs.BoolVar(&global.Verbose, "verbose", false, "print git commands")

	if err := fs.Parse(args); err != nil {
		return global, nil, err
	}
	return global, fs.Args(), nil
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

func runClone(ctx context.Context, runner git.Runner, global globalFlags, args []string, stdout io.Writer) (int, error) {
	if len(args) < 1 || len(args) > 2 {
		return 2, errors.New("usage: mwt clone URL [DIR]")
	}
	url := args[0]
	dir := ""
	if len(args) == 2 {
		dir = args[1]
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	cloneArgs := []string{"-C", cwd, "clone", url}
	if dir != "" {
		cloneArgs = append(cloneArgs, dir)
	}
	if runner.Verbose && runner.Logger != nil {
		runner.Logger("$ git %s", strings.Join(cloneArgs, " "))
	}
	cmd := exec.CommandContext(ctx, "git", cloneArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return 1, fmt.Errorf("git clone failed: %w", err)
	}

	cfg, err := config.Load(".", global.ConfigPath)
	if err != nil {
		return 1, fmt.Errorf("clone succeeded but config could not be loaded: %w", err)
	}
	root := filepath.Dir(cfg.FilePath)
	report, err := mwt.SyncWorkspace(ctx, runner, root, cfg)
	if err != nil {
		return 1, err
	}
	planner := mwt.Planner{Config: report.Config, Git: runner}
	plan, err := planner.PlanFetch(ctx, report.Config.BaseBranch)
	if err != nil {
		return 1, err
	}
	if err := planner.ExecuteFetch(ctx, plan, false); err != nil {
		return 1, err
	}
	if err := planner.Config.Save(""); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "cloned %s and synced workspace to %s\n", url, plan.Branch)
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
