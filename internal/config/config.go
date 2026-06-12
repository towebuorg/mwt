package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var candidateNames = []string{
	"mwt.yaml",
	"mwt.yml",
	".mwt.yaml",
	".mwt.yml",
}

type Config struct {
	FilePath     string
	WorktreeRoot string          `yaml:"worktree_root"`
	BaseBranch   string          `yaml:"base_branch"`
	Repos        map[string]Repo `yaml:"repos"`
}

type Repo struct {
	Path       string            `yaml:"path"`
	BaseBranch string            `yaml:"base_branch,omitempty"`
	Branch     string            `yaml:"branch,omitempty"`
	Remotes    map[string]string `yaml:"remotes,omitempty"`
}

func Load(startDir, explicitPath string) (*Config, error) {
	path, err := resolveConfigPath(startDir, explicitPath)
	if err != nil {
		return nil, err
	}
	return LoadFile(path)
}

func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.FilePath = path

	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func DefaultPath(startDir string) (string, error) {
	return filepath.Abs(filepath.Join(startDir, "mwt.yaml"))
}

func resolveConfigPath(startDir, explicitPath string) (string, error) {
	if explicitPath != "" {
		return filepath.Abs(explicitPath)
	}

	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}

	for {
		for _, name := range candidateNames {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", errors.New("could not find config file; use --config or add mwt.yaml")
}

func (c *Config) normalize() error {
	if c.WorktreeRoot == "" {
		return errors.New("config: worktree_root is required")
	}
	if c.BaseBranch == "" {
		c.BaseBranch = "main"
	}
	if len(c.Repos) == 0 {
		return errors.New("config: repos is required")
	}

	configDir := filepath.Dir(c.FilePath)
	root, err := expandPath(c.WorktreeRoot, configDir)
	if err != nil {
		return fmt.Errorf("config worktree_root: %w", err)
	}
	c.WorktreeRoot = root

	for name, repo := range c.Repos {
		if strings.TrimSpace(name) == "" {
			return errors.New("config: repo name cannot be empty")
		}
		if repo.Path == "" {
			return fmt.Errorf("config repo %q: path is required", name)
		}
		resolved, err := expandPath(repo.Path, configDir)
		if err != nil {
			return fmt.Errorf("config repo %q path: %w", name, err)
		}
		repo.Path = resolved
		if repo.BaseBranch == "" {
			repo.BaseBranch = c.BaseBranch
		}
		if repo.Remotes == nil {
			repo.Remotes = map[string]string{}
		}
		c.Repos[name] = repo
	}

	return nil
}

func (c *Config) Save(path string) error {
	if path == "" {
		if c.FilePath == "" {
			return errors.New("config save path is required")
		}
		path = c.FilePath
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve save path: %w", err)
	}
	configDir := filepath.Dir(absPath)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	out := Config{
		FilePath:     absPath,
		WorktreeRoot: relativeForSave(configDir, c.WorktreeRoot),
		BaseBranch:   c.BaseBranch,
		Repos:        map[string]Repo{},
	}

	names := make([]string, 0, len(c.Repos))
	for name := range c.Repos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		repo := c.Repos[name]
		copyRepo := Repo{
			Path:       relativeForSave(configDir, repo.Path),
			BaseBranch: repo.BaseBranch,
			Branch:     repo.Branch,
		}
		if len(repo.Remotes) > 0 {
			copyRepo.Remotes = map[string]string{}
			remoteNames := make([]string, 0, len(repo.Remotes))
			for remote := range repo.Remotes {
				remoteNames = append(remoteNames, remote)
			}
			sort.Strings(remoteNames)
			for _, remote := range remoteNames {
				copyRepo.Remotes[remote] = repo.Remotes[remote]
			}
		}
		out.Repos[name] = copyRepo
	}

	data, err := yaml.Marshal(&out)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(absPath, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	c.FilePath = absPath
	return nil
}

func (c *Config) NormalizeAfterSave() error {
	return c.normalize()
}

func expandPath(raw, baseDir string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if strings.HasPrefix(raw, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		switch raw {
		case "~":
			raw = home
		default:
			if strings.HasPrefix(raw, "~/") {
				raw = filepath.Join(home, raw[2:])
			}
		}
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	return filepath.Abs(filepath.Join(baseDir, raw))
}

func relativeForSave(baseDir, path string) string {
	if path == "" {
		return path
	}
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return path
	}
	if rel == "." {
		return "./"
	}
	if strings.HasPrefix(rel, "..") {
		return path
	}
	if strings.HasPrefix(rel, string(filepath.Separator)) {
		return path
	}
	return "./" + filepath.ToSlash(rel)
}
