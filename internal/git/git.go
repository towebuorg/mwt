package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Runner struct {
	Verbose bool
	Logger  func(format string, args ...any)
}

func (r Runner) Run(ctx context.Context, repo string, args ...string) error {
	_, _, err := r.run(ctx, repo, args...)
	return err
}

func (r Runner) Output(ctx context.Context, repo string, args ...string) (string, error) {
	stdout, _, err := r.run(ctx, repo, args...)
	return strings.TrimSpace(stdout), err
}

func (r Runner) run(ctx context.Context, repo string, args ...string) (string, string, error) {
	cmdArgs := append([]string{"-C", repo}, args...)
	if r.Verbose && r.Logger != nil {
		r.Logger("$ git %s", shellJoin(cmdArgs))
	}

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("git -C %s %s: %w\n%s", repo, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), stderr.String(), nil
}

func shellJoin(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || strings.ContainsAny(part, " \t\n\"'\\") {
			quoted = append(quoted, fmt.Sprintf("%q", part))
			continue
		}
		quoted = append(quoted, part)
	}
	return strings.Join(quoted, " ")
}
