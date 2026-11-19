# git-remote-sync

A utility to keep a remote git repository in sync with your local one via SSH, without using `git push`.

> **Designed for air-gapped z/OS systems.** The remote repositories cannot reach
> the outside world (no GitHub, no origin, no `git fetch`); the only connection is
> your own SSH access to them. `git-remote-sync` therefore never asks the remote to
> contact anything: commits travel as **git bundles over SSH**, files travel as
> **pax (z/OS) or tar (other POSIX systems) streams over SSH**, and the remote only
> needs `git` and the tools z/OS ships with. Non-z/OS POSIX remotes are detected
> automatically and are served with plain `tar`.

This tool maintains the exact state of your local repository on the remote, including:

- Current branch
- Modified files
- Untracked files
- Complete .git directory state

## Features

- **Direct SSH synchronization** - No need for git push/pull
- **Flexible configuration** - Configure via git config properties (`remote-sync.*`), config file (`._remote_sync`), or command-line flags
- **Built-in MCP Server** - Model Context Protocol server (`git-remote-sync-mcp` or `-mcp`) for seamless AI agent/harness integration
- **Air-gap ready** - The remote never contacts origin; missing commits are shipped as git bundles over SSH (v1.6.0+)
- **Three directions** - `l2r` (local to remote), `r2l` (remote to local) and `merge` (both ways, by history and timestamp) (v1.6.0+)
- **Commit-level sync** - Ensures remote is at the same commit as local using direct ref updates
- **Symlink sync** - Explicitly recreates git-tracked symlinks on the remote (v1.4.4+)
- **Optimized network traffic** - Skips sync when already at same commit (zero traffic)
- **Verbose mode** - Show all git commands with `-v` flag (L: local, R: remote)
- **z/OS support** - Remote OS is probed; z/OS gets pax archives (binary-tagged, AUTOCVT aware, as in z-scp) and fully qualified `/bin` or `/usr/bin` tools, never the zopen versions on PATH
- **Plain POSIX support** - On any other remote system `tar` is used
- **Complete state sync** - Syncs working tree and commit history
- **Batch processing** - Efficient transfer of multiple files
- **Cleanup** - Removes files on remote that don't exist locally
- **Remote environment setup** - Run setup commands before each remote operation

## Installation

```bash
cd git-remote-sync
go build -o git-remote-sync .
ln -sf git-remote-sync git-remote-sync-mcp
sudo cp git-remote-sync /usr/local/bin/  # Optional: install system-wide
```

With `git-remote-sync` on your PATH, git finds it as a subcommand, so
`git remote-sync` works from any repository (see [Basic Usage](#basic-usage)).

## Configuration

Configuration can be provided via command-line flags, a config file (`._remote_sync`), git config properties (local or global), or any combination.

### Option 1: Git Config Properties

You can configure git properties in local repository config (`.git/config`) or globally (`~/.gitconfig`):

```bash
# Set for the current repository (local)
git config remote-sync.remote-path "userid@hostname:/path/to/remote/repo"
git config remote-sync.remote-setup ". ./.env"

# Or set globally
git config --global remote-sync.remote-setup ". ~/.env"
```

Supported git config properties:
- `remote-sync.remote-path`: Remote connection string in format `[user@]host[:path]`
- `remote-sync.remote-setup`: Optional setup/environment command executed before remote operations

### Option 2: Configuration File

Create a file named `._remote_sync` in your git repository root (this file should NOT be committed to git):

```
remote-path:userid@hostname:/path/to/remote/repo
remote-setup:. ./.env
```

The `remote-path` line specifies the SSH connection and remote repository path. It supports multiple formats:
- `userid@hostname:/path/to/remote/repo` - Explicit user, hostname, and path
- `hostname:/path/to/remote/repo` - Hostname and path (useful with SSH config entries that define the user)
- `userid@hostname` - Explicit user and hostname (path from SSH config or current directory)
- `hostname` - Hostname only (useful with SSH config entries that define user and default path)

The `remote-setup` line is optional and specifies commands to run before each remote operation (e.g., sourcing environment files).

Example with explicit user and environment setup:
```
remote-path:john@mainframe.example.com:/home/john/myproject
remote-setup:. ./.env
```

Example with SSH config host entry (no user specified):
```
remote-path:mainframe:/home/john/myproject
remote-setup:. ./.env
```

Example with hostname only (using SSH config for user and path):
```
remote-path:mainframe
remote-setup:. ./.env
```

Example without remote setup:
```
remote-path:john@server.example.com:/home/john/myproject
```

Add to `.gitignore`:
```bash
echo "._remote_sync" >> .gitignore
```

### Option 3: Command-Line Flags

You can provide configuration via command-line flags, which override the config file:

```bash
# Provide all configuration via command line with explicit user
git-remote-sync -remote-path user@host:/path/to/repo -remote-setup ". ./.env"

# Use SSH config host entry (no user specified)
git-remote-sync -remote-path myhost:/path/to/repo -remote-setup ". ./.env"

# Override only the remote path from config file
git-remote-sync -remote-path user@host:/different/path

# Override only the setup command
git-remote-sync -remote-setup ". /custom/setup.sh"
```

### Option 4: Precedence & Combination

When settings are present in multiple sources, precedence is resolved in order (highest to lowest):
1. **Command-line flags** (`-remote-path`, `-remote-setup`)
2. **Config file** (`._remote_sync`)
3. **Git config properties** (`remote-sync.remote-path`, `remote-sync.remote-setup` — local overrides global)

```bash
# Git config provides default remote-path, override setup command on CLI
git-remote-sync -remote-setup ". /tmp/test-env"
```

## Usage

### Basic Usage

From anywhere within your git repository:

```bash
git-remote-sync
```

Because git runs any executable named `git-<name>` on the PATH as a subcommand,
once `git-remote-sync` is on your PATH you can also invoke it as a git command:

```bash
git remote-sync                      # same as git-remote-sync
git remote-sync -direction merge -v  # options work the same way
```

The utility will:
1. Read configuration from file and/or command-line flags
2. Verify SSH connectivity
3. Run remote-setup commands (if configured)
4. Probe the remote (z/OS or other POSIX, location of the native tools)
5. Ship missing commits as a git bundle over SSH and move the target HEAD
6. Revert remote-only changes (files modified on remote but clean locally, v1.4.5+)
7. Recreate git-tracked symlinks on the remote (v1.4.4+)
8. Sync all modified and untracked files
9. Clean up files on remote that don't exist locally
10. Verify git status consistency
11. Display a summary

### Command-Line Options

```bash
git-remote-sync -h              # Show help
git-remote-sync --version       # Show version, commit hash, and date
git-remote-sync -mcp            # Run as Model Context Protocol (MCP) server over stdio
git-remote-sync -v              # Verbose mode (show all git commands)
git-remote-sync -remote-path string    # Remote path (user@host:/path)
git-remote-sync -remote-setup string   # Remote setup command
git-remote-sync -direction l2r|r2l|merge   # Sync direction (default l2r)
git-remote-sync -force          # l2r/r2l: allow overwriting a non fast-forward target branch
```

### Sync Directions (v1.6.0+)

| Direction | Aliases | Effect |
|---|---|---|
| `l2r` (default) | `local-to-remote`, `push` | The remote becomes a copy of local: commits, branch, modified and untracked files. Remote-only changes are reverted. |
| `r2l` | `remote-to-local`, `pull` | Local becomes a copy of the remote (hard reset to the remote HEAD, remote-modified and untracked files downloaded, stale local files removed). Local-only commits and changes are discarded. |
| `merge` | `merge-remote-local` | Commits are exchanged both ways. If histories diverged, a merge commit is created (aborts on conflicts). Uncommitted paths changed on only one side take that side's version; a path changed on both sides keeps an existing file over a deleted one, then the **newer mtime wins** (tie: local). Both sides end at the same commit and the same working tree. |

`l2r` and `r2l` make the target a mirror of the source, but they **refuse to drop commits**: if the target branch (or HEAD) holds commits that are not in the new history (not a fast-forward), the sync stops and lists them (up to 10). Use `-direction merge` to combine both histories, or `-force` to overwrite anyway; the old tip is then saved as `refs/remote-sync/previous/<sha>` on the overwritten side. Uncommitted changes on the target side are still discarded.
`merge` compares file times with a one-second resolution using the remote
clock for remote files, so large clock differences between the machines can
pick the wrong side. `._remote_sync` is never deleted.

### Model Context Protocol (MCP) Server

`git-remote-sync` can be run directly as a Model Context Protocol (MCP) server over standard I/O (stdio JSON-RPC 2.0). This enables AI agents, coding assistants, and automated harnesses (such as Bob, Claude Desktop, Cursor, OpenCode, VS Code MCP extensions, etc.) to inspect configuration and synchronize repositories programmatically without running shell commands.

The server launches in MCP mode when invoked via the `git-remote-sync-mcp` executable (or symlink) or when passed the `-mcp` flag (`git-remote-sync -mcp`).

#### Tools Exposed to AI Agents

| Tool | Purpose | Parameters |
|---|---|---|
| `git_remote_sync` | Triggers synchronization of the local repo state to the remote system. | `repo_path` (string, optional - defaults to current working directory)<br>`remote_path` (string, optional - overrides config/git property)<br>`remote_setup` (string, optional - overrides setup command)<br>`direction` (string, optional - `l2r` default, `r2l`, `merge`)<br>`force` (boolean, optional - allow non fast-forward overwrite in l2r/r2l)<br>`verbose` (boolean, optional - show command trace) |
| `git_remote_sync_config_get` | Inspects the effective repository sync config, reporting resolved values and detailing what was found in `._remote_sync`, local `.git/config`, and global `~/.gitconfig`. | `repo_path` (string, optional) |
| `git_remote_sync_config_set` | Sets `remote-sync.remote-path` or `remote-sync.remote-setup` in git config. | `repo_path` (string, optional)<br>`remote_path` (string, optional)<br>`remote_setup` (string, optional)<br>`global` (boolean, default `false` - sets in `~/.gitconfig` if `true`) |
| `git_remote_sync_help` | Returns a complete quick-reference guide and configuration syntax for the agent. | *(none)* |

#### AI Agent & Harness Configuration Examples

##### 1. Bob / IBM Bob (`.bob/mcp.json` or `~/.bob/mcp.json`)
```json
{
  "mcpServers": {
    "git-remote-sync": {
      "command": "/home/knoppix/go-dev/git-remote-sync/git-remote-sync-mcp",
      "args": []
    }
  }
}
```

##### 2. Claude Desktop (`claude_desktop_config.json`)
```json
{
  "mcpServers": {
    "git-remote-sync": {
      "command": "/usr/local/bin/git-remote-sync-mcp",
      "args": []
    }
  }
}
```

##### 3. OpenCode (`opencode.json`)
```json
{
  "mcp": {
    "git-remote-sync": {
      "type": "local",
      "command": ["git-remote-sync-mcp"],
      "enabled": true
    }
  }
}
```

##### 4. Generic Stdio Harness / Subprocess Spawner
Launch command: `git-remote-sync-mcp` (or `git-remote-sync -mcp`) with standard JSON-RPC 2.0 lines on stdin/stdout. Logging is isolated to stderr.

**Version Information:**
```bash
$ git-remote-sync --version
git-remote-sync version 1.5.0
```

### Usage Examples

```bash
# Configure once via git config (local repository)
git config remote-sync.remote-path "user@host:/path/to/repo"
git config remote-sync.remote-setup ". ./.env"
git-remote-sync

# Configure default setup globally across all repos
git config --global remote-sync.remote-setup ". ~/.env"

# Use existing ._remote_sync config file or git config
git-remote-sync

# Provide all config via command line with explicit user
git-remote-sync -remote-path user@host:/path -remote-setup ". ./.env"

# Use SSH config host entry (no user in path)
git-remote-sync -remote-path myhost:/path -remote-setup ". ./.env"

# Override remote path from config file
git-remote-sync -remote-path user@testhost:/test/path

# Use config file but override setup command
git-remote-sync -remote-setup ". /custom/env.sh"
```

## How It Works

### Remote Environment Setup

If you specify a `remote-setup` command, it will be executed before each remote operation:

```bash
cd /remote/path && . ./.env && <actual command>
```

This is useful for:
- Sourcing environment files
- Setting up PATH variables
- Loading shell configurations
- Activating virtual environments

### Remote Probe

Right after connecting, one SSH call runs `uname`. If the remote is z/OS
(`OS/390`), the native tools (`cat rm mkdir ln pax find touch chtag iconv`) are
located in `/bin` or `/usr/bin` and always called by their fully qualified path,
because the PATH set up by the remote-setup command often prefers other versions
(zopen). `git` itself is resolved from PATH. Any other system is treated as plain
POSIX: `tar` and `cat` from PATH are used and no tool paths are fixed.

### Commits Without Origin

The remote cannot fetch. For each sync the tool asks which commits the remote lacks
(`git cat-file --batch-check` on the remote), builds a bundle of only those
commits locally (`git bundle create`, with the remote's tips as prerequisites),
uploads it over SSH to `.git/remote-sync.bundle`, runs `git bundle unbundle` there
and removes the file. For `r2l` and `merge` the same happens in the other direction.
If the remote history cannot be related to the local one, a full bundle is sent.

### For Unix/Linux Remote Systems

Files and bundles are transferred using tar over SSH:
```bash
tar -cf - files | ssh user@host 'cd /path && tar -xf -'
```

### For z/OS Remote Systems

Files and bundles go through `pax` with the `_BPXK_AUTOCVT=ON` handling used by
[z-scp](../z-scp): the archive carries a `ZOS.taginfo` header (binary, `0 65535`)
and the EBCDIC/ASCII translation done by AUTOCVT on the SSH stream is compensated
locally, so the bytes arrive unchanged. Downloads translate back and convert files
tagged as IBM-1047 text to ISO-8859-1:
```bash
# upload  (local: ASCII->EBCDIC table applied to the archive)
ssh user@host 'cd /path && _BPXK_AUTOCVT=ON /bin/pax -r -x pax -p p'
# download
ssh user@host 'cd /path && _BPXK_AUTOCVT=ON /bin/pax -w -x pax file ...'
```

## Requirements


### Local System
- Git
- SSH client
- tar
- Go 1.16+ (for building)

### Remote System
- SSH server
- Git repository initialized at the target path
- z/OS: `pax`, `cat`, `rm`, `mkdir`, `ln`, `find`, `touch` in `/bin` or `/usr/bin`; `git` on PATH after the setup command
- Other POSIX systems: `tar`
- **No outbound network access is needed on the remote**

## Example Workflow

### Workflow A: Using Git Config Properties (Recommended)

```bash
# 1. Set remote path and environment setup in git config
git config remote-sync.remote-path user@remote:/home/user/project
git config remote-sync.remote-setup ". ./.env"

# 2. Make changes locally
echo "new feature" > feature.txt
git add feature.txt

# 3. Sync to remote (without git push)
git-remote-sync
```

### Workflow B: Using `._remote_sync` Config File

```bash
# 1. Set up remote configuration file
cat > ._remote_sync << EOF
remote-path:user@remote:/home/user/project
remote-setup:. ./.env
EOF

# 2. Make changes locally
echo "new feature" > feature.txt
git add feature.txt

# 3. Sync to remote (without git push)
git-remote-sync

# Output:
# Git repository root: /home/user/myproject
# Remote: user@remote:/home/user/project
# Current branch: main
# 
# Starting sync...
# Files to sync: 42
# 
# Syncing .git directory...
# ✓ .git directory synced
# 
# Syncing working tree files...
# ✓ Synced files 1-42 of 42
# 
# Cleaning up remote files...
# ✓ No files to clean up
# 
# ==================================================
# Sync Summary
# ==================================================
# Files transferred: 42
# Duration: 2.3s
# 
# ✓ Sync completed successfully!
```

## Troubleshooting

### SSH Connection Issues

Ensure you can SSH to the remote without password:
```bash
ssh-copy-id user@remote
ssh user@remote 'echo OK'
```

### Remote Setup Command Fails

Test your remote-setup command manually:
```bash
ssh user@remote 'cd /path/to/repo && . ./.env && echo OK'
```

### z/OS EBCDIC Conversion

If you get encoding errors on z/OS, check the native tools the probe uses:
```bash
ssh user@zos 'ls -l /bin/pax /bin/iconv /bin/chtag'
```
Run with `-v` to see the probe result and every remote command.

### Remote Path Not Found

Ensure the remote path exists and is a git repository:
```bash
ssh user@remote 'test -d /path/to/repo/.git && echo OK'
```

## Configuration Reference

### Git Config Properties

| Property | Description | Format / Example | Scope |
|---|---|---|---|
| `remote-sync.remote-path` | Target SSH user, host, and remote repository path. | `[user@]host[:path]`<br>`ccw@pok56:/home/ccw/delve`<br>`pok56:/home/ccw/delve`<br>`pok56` | Local (`--local`) or Global (`--global`) |
| `remote-sync.remote-setup` | Setup command executed before remote git operations (e.g. environment sourcing). | `string`<br>`. ./.env`<br>`. ~/.env` | Local (`--local`) or Global (`--global`) |

Commands:
```bash
# Local to repo (.git/config)
git config remote-sync.remote-path "user@host:/path/to/repo"
git config remote-sync.remote-setup ". ./.env"

# Global (~/.gitconfig)
git config --global remote-sync.remote-setup ". ~/.env"

# Inspect current configuration
git config --get-regexp remote-sync
```

### Configuration File Format (`._remote_sync`)

The `._remote_sync` file supports the following keys:

- `remote-path`: SSH connection string in format `[user@]host[:path]`
- `remote-setup`: Shell command to run before each remote operation

Lines starting with `#` are treated as comments and ignored.

## Limitations

- Requires SSH access to remote
- Remote must have git, and pax (z/OS) or tar (other POSIX systems); the remote repository must already be initialised (`git init`)
- `r2l` and `l2r` overwrite the target working tree (and, with `-force`, its branch); `merge` relies on comparable clocks
- Large repositories may take time to sync initially
- Does not handle git submodules specially
- Symlinks are recreated unconditionally on every sync (fast, but adds one SSH round-trip)

## Security Notes

- The `._remote_sync` file contains connection information - keep it secure
- Add `._remote_sync` to `.gitignore` to prevent accidental commits
- Uses your SSH keys for authentication
- No passwords are stored
- Remote setup commands are executed with your SSH user privileges

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
