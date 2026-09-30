# git-remote-sync

A utility to keep a remote git repository in sync with your local one via SSH, without using `git push`. This tool maintains the exact state of your local repository on the remote, including:

- Current branch
- Modified files
- Untracked files
- Complete .git directory state

## Features

- **Direct SSH synchronization** - No need for git push/pull
- **Flexible configuration** - Configure via git config properties (`remote-sync.*`), config file (`._remote_sync`), or command-line flags
- **Built-in MCP Server** - Model Context Protocol server (`git-remote-sync-mcp` or `-mcp`) for seamless AI agent/harness integration
- **Automatic push detection** - Detects unpushed commits and pushes them to origin automatically (v1.4.0+)
- **Safe push validation** - Only pushes if fast-forward is possible, fails on divergence
- **Commit-level sync** - Ensures remote is at the same commit as local using direct ref updates
- **Symlink sync** - Explicitly recreates git-tracked symlinks on the remote (v1.4.4+)
- **Optimized network traffic** - Skips sync when already at same commit (zero traffic)
- **Verbose mode** - Show all git commands with `-v` flag (L: local, R: remote)
- **z/OS support** - Smart file transfer with automatic fallback (direct transfer or iconv conversion)
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

The utility will:
1. Read configuration from file and/or command-line flags
2. Verify SSH connectivity
3. Run remote-setup commands (if configured)
4. Check for unpushed commits and push them to origin if safe (v1.4.0+)
5. Sync commit history to remote
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
```

### Model Context Protocol (MCP) Server

`git-remote-sync` can be run directly as a Model Context Protocol (MCP) server over standard I/O (stdio JSON-RPC 2.0). This enables AI agents, coding assistants, and automated harnesses (such as Bob, Claude Desktop, Cursor, OpenCode, VS Code MCP extensions, etc.) to inspect configuration and synchronize repositories programmatically without running shell commands.

The server launches in MCP mode when invoked via the `git-remote-sync-mcp` executable (or symlink) or when passed the `-mcp` flag (`git-remote-sync -mcp`).

#### Tools Exposed to AI Agents

| Tool | Purpose | Parameters |
|---|---|---|
| `git_remote_sync` | Triggers synchronization of the local repo state to the remote system. | `repo_path` (string, optional - defaults to current working directory)<br>`remote_path` (string, optional - overrides config/git property)<br>`remote_setup` (string, optional - overrides setup command)<br>`verbose` (boolean, optional - show command trace) |
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

### For Unix/Linux Remote Systems

Files are transferred using tar over SSH:
```bash
tar -cf - files | ssh user@host 'cd /path && tar -xf -'
```

### For z/OS Remote Systems

The utility automatically detects z/OS and uses `iconv` for EBCDIC/ASCII conversion (codepage 1047 to 819):
```bash
tar -cf - files | ssh user@host 'cd /path && /bin/iconv -f 1047 -t 819 | /bin/tar -xvfUX -'
```

## Requirements


### Automatic Push Detection (v1.4.0+)

The tool automatically detects unpushed local commits and handles them intelligently:

**What it does:**
1. Compares local HEAD with `origin/<branch>`
2. If commits are unpushed and push is safe (fast-forward), automatically pushes to origin
3. Triggers remote to fetch the newly pushed commits
4. Continues with normal sync

**Safety checks:**
- Only pushes if it's a fast-forward (no force push)
- If branch has diverged from origin, exits with error and instructions
- Skips push if no origin remote is configured

**Example output:**
```bash
Origin: git@github.com:user/repo.git
Pushing local commits to origin/main...
✓ Pushed commits to origin
Fetching newly pushed commits on remote...
✓ Remote fetched newly pushed commits
✓ Synced commits to: a1b2c3d4
```

**Divergence handling:**
If your local branch has diverged from origin (would require force push):
```
Error: local branch has diverged from origin/main - force push required.
Please resolve manually:
  git push --force-with-lease origin main
```


### Local System
- Git
- SSH client
- tar
- Go 1.16+ (for building)

### Remote System
- SSH server
- Git repository initialized at the target path
- tar (or /bin/tar on z/OS)
- `iconv` (on z/OS systems, typically at /bin/iconv)

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

If you get encoding errors on z/OS, ensure `iconv` is available:
```bash
ssh user@zos 'which /bin/iconv'
```

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
- Remote must have tar (or /bin/tar)
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
