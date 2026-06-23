# git-remote-sync

A utility to keep a remote git repository in sync with your local one via SSH, without using `git push`. This tool maintains the exact state of your local repository on the remote, including:

- Current branch
- Modified files
- Untracked files
- Complete .git directory state

## Features

- **Direct SSH synchronization** - No need for git push/pull
- **Automatic push detection** - Detects unpushed commits and pushes them to origin automatically (v1.4.0+)
- **Safe push validation** - Only pushes if fast-forward is possible, fails on divergence
- **Commit-level sync** - Ensures remote is at the same commit as local using direct ref updates
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
go build -o git-remote-sync
sudo cp git-remote-sync /usr/local/bin/  # Optional: install system-wide
```

## Configuration

Configuration can be provided via a config file, command-line flags, or both (flags override file settings).

### Option 1: Configuration File

Create a file named `._remote_sync` in your git repository root (this file should NOT be committed to git):

```
remote-path:userid@hostname:/path/to/remote/repo
remote-setup:. ./.env
```

The `remote-path` line specifies the SSH connection and remote repository path.
The `remote-setup` line is optional and specifies commands to run before each remote operation (e.g., sourcing environment files).

Example with environment setup:
```
remote-path:john@mainframe.example.com:/home/john/myproject
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

### Option 2: Command-Line Flags

You can provide configuration via command-line flags, which override the config file:

```bash
# Provide all configuration via command line (no config file needed)
git-remote-sync -remote-path user@host:/path/to/repo -remote-setup ". ./.env"

# Override only the remote path from config file
git-remote-sync -remote-path user@host:/different/path

# Override only the setup command
git-remote-sync -remote-setup ". /custom/setup.sh"
```

### Option 3: Combination

Use a config file for defaults and override specific settings with flags:

```bash
# Config file has remote-path, override setup command
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
6. Sync all tracked and untracked files
7. Clean up files on remote that don't exist locally
8. Verify git status consistency
9. Display a summary

### Command-Line Options

```bash
git-remote-sync -h              # Show help
git-remote-sync --version       # Show version, commit hash, and date
git-remote-sync -v              # Verbose mode (show all git commands)
git-remote-sync -remote-path string    # Remote path (user@host:/path)
git-remote-sync -remote-setup string   # Remote setup command
```

**Version Information:**
```bash
$ git-remote-sync --version
git-remote-sync version 1.4.1
commit: 3b6a5b2
date: 2026-06-23 17:25:36 -0400
```

### Usage Examples

```bash
# Use config file
git-remote-sync

# Provide all config via command line
git-remote-sync -remote-path user@host:/path -remote-setup ". ./.env"

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

```bash
# 1. Set up remote configuration
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

## Configuration File Format

The `._remote_sync` file supports the following keys:

- `remote-path` (required): SSH connection string in format `user@host:/path`
- `remote-setup` (optional): Shell command to run before each remote operation

Lines starting with `#` are treated as comments and ignored.

## Limitations

- Requires SSH access to remote
- Remote must have tar (or /bin/tar)
- Large repositories may take time to sync initially
- Does not handle git submodules specially

## Security Notes

- The `._remote_sync` file contains connection information - keep it secure
- Add `._remote_sync` to `.gitignore` to prevent accidental commits
- Uses your SSH keys for authentication
- No passwords are stored
- Remote setup commands are executed with your SSH user privileges

## License

MIT License - feel free to use and modify as needed.
