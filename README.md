# mwt

`mwt` is a small Go CLI for coordinating matching Git worktrees across multiple independent repositories.

Website: [mwt.towebu.com](https://mwt.towebu.com/)

It now covers two layers:

- workspace management for discovering and aligning canonical repositories
- worktree management for creating, inspecting, merging, and removing matching feature worktrees

Multi-repo operations are not atomic. `mwt` is intentionally explicit and Git-shaped.

## License

MIT. See [LICENSE](./LICENSE).

## Install

```bash
go install .
```

Or build a local binary:

```bash
go build -buildvcs=false .
```

After the first tagged release is published, Homebrew installation is:

```bash
brew tap towebuorg/tap
brew install --cask mwt
```

## Config

`mwt` searches the current directory and parents for:

- `mwt.yaml`
- `mwt.yml`
- `.mwt.yaml`
- `.mwt.yml`

You can also pass `--config PATH`.

The config can be created and maintained by `mwt init` and `mwt sync`.

Example:

```yaml
worktree_root: ./worktrees
base_branch: main

repos:
  backend:
    path: ./repos/backend
    base_branch: main
    branch: main
    remotes:
      origin: git@github.com:example/backend.git
  frontend:
    path: ./repos/frontend
    base_branch: main
    branch: main
    remotes:
      origin: git@github.com:example/frontend.git
  infra:
    path: ./repos/infra
    base_branch: main
    branch: main
    remotes:
      origin: git@github.com:example/infra.git
```

`path` is stored relative to the config file when possible. `worktree_root` supports relative paths and `~/...`.

## Commands

### `mwt init`

Discovers Git repositories under the current directory, skipping nested repositories, and writes `mwt.yaml`.

For each repo it records:

- relative path
- current branch
- configured remotes

The initial `base_branch` defaults to the common current branch when all discovered repos match, otherwise `main`.

### `mwt sync`

Re-discovers repositories under the current directory and updates the config.

- newly discovered repos are added
- existing repos have branch and remote metadata refreshed
- removed repos are dropped from the config

### `mwt fetch [BRANCH] [--yes]`

Fetches all configured canonical repositories:

```bash
git -C <repo> fetch --all --prune
```

Then checks each repo out to the target branch. If `BRANCH` is omitted, `base_branch` is used.

If any repo is dirty, `mwt` asks for confirmation before a forced checkout. This is intentionally explicit because it can discard local changes in canonical repos.

### `mwt clone URL [DIR]`

Clones a new repository into the current workspace, runs `sync`, then runs `fetch` against the configured `base_branch` so the workspace returns to a common branch baseline.

`mwt` cannot change your parent shell directory, so “enter the directory” is handled internally as part of the clone-and-sync flow.

### `mwt create NAME`

Creates matching feature worktrees:

```text
worktrees/NAME/backend
worktrees/NAME/frontend
worktrees/NAME/infra
```

For each repo:

```bash
git -C <repo-path> worktree add -b <name> <worktree-path> <base-branch>
```

If the feature branch already exists:

```bash
git -C <repo-path> worktree add <worktree-path> <name>
```

If creation fails partway through, `mwt` prints exactly which worktrees were created and suggests `mwt remove NAME`.

### `mwt status NAME`

Shows worktree state across repos:

```text
backend    feature-auth clean +1/-0
frontend   feature-auth dirty +0/-2
  M src/app.ts
infra      feature-auth clean +0/-0
```

### `mwt foreach NAME -- COMMAND...`

Runs a command inside each named worktree:

```bash
mwt foreach feature-auth -- git status --short
mwt foreach feature-auth -- npm test
```

By default it stops on first failure. Use `--keep-going` to continue.

### `mwt merge NAME`

Validates all worktrees first, then merges `NAME` back into each repo’s configured `base_branch`.

Useful flags:

- `--dry-run`
- `--yes`
- `--allow-dirty`

The merge steps per repo are:

```bash
git -C <repo-path> checkout <base-branch>
git -C <repo-path> merge --no-ff <name>
```

If a merge conflicts, `mwt` stops immediately, reports the repo, and leaves that repo in the conflicted state.

### `mwt remove NAME`

Removes the matching worktrees:

```bash
git -C <repo-path> worktree remove <worktree-path>
```

Flags:

- `--force`
- `--delete-branches`

### `mwt list`

Lists directories directly under `worktree_root`.

### `mwt snapshot NAME`

Prints the current commit of each named worktree:

```yaml
name: feature-auth
repos:
  backend:
    branch: feature-auth
    commit: abc123
  frontend:
    branch: feature-auth
    commit: def456
  infra:
    branch: feature-auth
    commit: 789abc
```

## Example workflow

Initialize a workspace from existing repos:

```bash
mwt init
mwt fetch
```

Add a new repo and realign everything:

```bash
mwt clone git@github.com:example/payments.git
```

Create a feature worktree set:

```bash
mwt create feature-auth
cd worktrees/feature-auth/backend
# edit code
cd ../frontend
# edit code

mwt status feature-auth
mwt foreach feature-auth -- git diff --stat
mwt snapshot feature-auth
mwt merge feature-auth --dry-run
mwt merge feature-auth --yes
mwt remove feature-auth
```

## Flags

- `--config PATH`: use a specific config file
- `--verbose`: print underlying Git commands
- `--no-color`: disable ANSI colors

## Releasing

Releases are managed by GoReleaser in `.goreleaser.yaml` and GitHub Actions in `.github/workflows/release.yml`.

Push a semver tag to publish a release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The release workflow:

- runs `go mod tidy`
- runs `go test ./...`
- builds Linux, macOS, and Windows binaries for `amd64` and `arm64`
- creates archives and `checksums.txt`
- builds a changelog from commits
- publishes a GitHub release in `towebuorg/mwt`
- updates the Homebrew cask in `towebuorg/homebrew-tap`

The Homebrew tap repository is `towebuorg/homebrew-tap`. Before the first release, add a repository secret named `HOMEBREW_TAP_GITHUB_TOKEN` to `towebuorg/mwt`. The token needs permission to push to the tap repository.

You can test the release config locally without publishing:

```bash
goreleaser release --snapshot --clean
```

## Intentionally Not Supported Yet

- Parallel execution across repos.
- Remote selection policies more advanced than “prefer an existing local branch, otherwise use the first remote that has the branch”.
- Snapshot restore or replay.
- Per-repo feature branch names under one multi-worktree name.
- Automatic push/rebase orchestration.
- Worktree discovery for repos that are themselves already stored as Git worktrees instead of canonical clones.
