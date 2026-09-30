# git-remote-sync Technical Documentation

## Architecture Overview

### Design Philosophy
The utility is designed as a single-binary Go application that synchronizes git repository working trees between local and remote systems via SSH. It avoids using `rsync` and instead relies on `tar` over SSH with optional EBCDIC/ASCII conversion for z/OS systems.

### Core Components

```
main()
  ├── getGitRoot()
  ├── readRemoteConfig()
  ├── getCurrentBranch()
  ├── verifyRemote()
  └── syncRepository()
      ├── ensureRemoteBranch()
      ├── syncCommitsViaBundle()
      ├── discardRemoteOnlyChanges() ← v1.4.5
      ├── syncSymlinks()           ← v1.4.4
      ├── getFilesToSync()
      └── syncFiles()
          ├── filterFilesNeedingSync()
          └── syncFileBatch() / syncSingleFileZOS()
```

## Configuration System

### Configuration Sources
Configuration can be provided via:
1. **Command-line flags**: Highest priority (`-remote-path`, `-remote-setup`)
2. **Config file**: `._remote_sync` in repository root (should be in `.gitignore`)
3. **Git config properties**: Local (`git config ...`) or global (`git config --global ...`)

### Git Config Properties
Set via standard `git config`:
```bash
# Local to repository (.git/config)
git config remote-sync.remote-path "userid@hostname:/path/to/remote/repo"
git config remote-sync.remote-setup ". ./.env"

# Or global (~/.gitconfig)
git config --global remote-sync.remote-setup ". ~/.env"
```

**Keys:**
- `remote-sync.remote-path`: SSH connection string in format `[user@]host[:path]`
- `remote-sync.remote-setup`: Shell command executed before each remote operation

### Configuration File Format
Location: `._remote_sync` in repository root (optional if using git config or command-line flags)

```
remote-path:userid@hostname:/path/to/remote/repo
remote-setup:. ./.env
```

**Key-Value Pairs:**
- `remote-path`: SSH connection string in format `[user@]host[:path]`
  - Supports `user@host:/path` (explicit user, hostname, and path)
  - Supports `host:/path` (hostname and path, useful with SSH config entries)
  - Supports `user@host` (explicit user and hostname, no path)
  - Supports `host` (hostname only, useful with SSH config entries that define user and default path)
- `remote-setup`: Shell command executed before each remote operation

### Command-Line Flags
```bash
-remote-path string    Remote path in format [user@]host[:path] (overrides config file / git config)
-remote-setup string   Remote setup command (overrides config file / git config)
-h, -help             Show help message
-v, -version          Show version information
```

**Examples:**
```bash
# Use git config or config file
git-remote-sync

# Override remote path with explicit user
git-remote-sync -remote-path user@host:/path/to/repo

# Override remote path using SSH config host entry
git-remote-sync -remote-path myhost:/path/to/repo

# Provide all config via command line (no config file or git config needed)
git-remote-sync -remote-path user@host:/path -remote-setup ". ./.env"

# Override only setup command
git-remote-sync -remote-setup ". /custom/setup.sh"
```

### Configuration Parsing
- Implemented in `readRemoteConfig(path, cmdRemotePath, cmdRemoteSetup)`
- Reads from `._remote_sync` file if present
- Reads from `git config remote-sync.*` properties via `getGitConfig`
- Command-line flags override both file and git config
- Supports comments (lines starting with `#`) in config file
- Ignores empty lines
- Uses `strings.SplitN(line, ":", 2)` to parse file key-value pairs
- Validates required fields before returning

**Priority Order:**
1. Command-line flags (highest priority)
2. Config file settings (`._remote_sync`)
3. Git config properties (`remote-sync.remote-path`, `remote-sync.remote-setup`)
4. Error if none provides required `remote-path`

## Remote Command Execution

### Command Building Strategy
Function: `buildRemoteCommand(remote *RemoteConfig, command string) string`

**Without Setup:**
```bash
cd /remote/path && <command>
```

**With Setup:**
```bash
. ./.env && cd /remote/path && <command>
```

**Critical Design Decision:** Setup command is executed BEFORE `cd` to ensure environment variables are available in the correct context.

### SSH Command Execution
All remote commands use: `exec.Command("ssh", "user@host", remoteCmd)`

**Error Handling:**
- `CombinedOutput()` for commands where we need both stdout and stderr
- `Output()` for commands where we only need stdout
- Avoid `2>&1` redirection on z/OS to prevent EBCDIC encoding issues in output

## File Synchronization Strategy

### Phase 0: Symlink Sync (v1.4.4+)
Function: `syncSymlinks(gitRoot string, remote *RemoteConfig, stats *SyncStats) error`

Runs immediately after `syncCommitsViaBundle`, before the regular file sync.

**Why a dedicated phase:**
Git stores symlinks as blobs with mode `120000` — the blob content is the symlink target path
(e.g. `CLAUDE.md`). On z/OS, `git reset --hard` may not correctly recreate these as filesystem
symlinks because z/OS USS symlink support is limited and some git builds do not handle mode
`120000` on z/OS. The result is that the file either doesn't exist or is created as a regular
file containing the target path as text, rather than a real symlink.

**Process:**
1. Run `git ls-files --stage -z` locally and filter entries with mode `120000`
2. For each symlink, read the target via `git show :<path>` (blob content = target path)
3. Build a single SSH command that does, for every symlink:
   ```bash
   rm -f <path> && ln -sf <target> <path>
   ```
4. Run the whole batch in one SSH round-trip

**Why unconditional recreation:**
- The total cost is one SSH call regardless of how many symlinks there are
- Avoids complex "does this symlink already point to the right target?" remote check
- Idempotent: `ln -sf` overwrites whatever is there

### Phase 1: File Discovery
Function: `getFilesToSync(gitRoot string) ([]string, error)`

**Process:**
1. Run `git status --porcelain` to find modified/staged files
2. Parse output format: `XY filename` or `XY old -> new` (for renames)
3. Run `git ls-files --others --exclude-standard` for untracked files
4. Combine into deduplicated set using `map[string]bool`

**Why This Approach:**
- Only syncs files that differ from HEAD
- Includes untracked files (important for development workflow)
- Respects `.gitignore` patterns
- Avoids syncing entire repository on every run

### Phase 2: Checksum Comparison
Function: `filterFilesNeedingSync(gitRoot, remote, files, isZOS) ([]string, error)`

**Local Checksums:**
```bash
shasum <file>
```

**Remote Checksums:**
```bash
for f in <files>; do 
  if [ -f "$f" ]; then 
    shasum "$f" 2>/dev/null
  fi
done
```

**Why shasum:**
- Available on both Unix and z/OS (via zopen)
- Consistent output format: `checksum  filename`
- SHA-1 is fast enough for this use case
- More portable than `sha256sum` or `/bin/sha256`

**Optimization:**
- Batch checksum operations (all files in one SSH call)
- Skip files with matching checksums
- Include files in sync list if checksum fails (safer than skipping)

### Phase 3: File Transfer

**Unix Systems:**
Function: `syncFileBatch(gitRoot, remote, files, stats) error`

Uses tar-based batch transfer for efficiency:
```bash
# Local: Create tar archive
cd <gitRoot> && tar -cf - -T <file_list>

# Transfer via SSH
| ssh user@host 'cd /remote/path && tar -xf -'
```

**z/OS Systems:**
Function: `syncSingleFileZOS(gitRoot, remote, file, stats) error`

Uses individual file transfer with smart fallback:
```bash
# Method 1: Direct transfer (try first)
cat local_file | ssh user@host 'cd /remote/path && cat > file'

# Method 2: With iconv conversion (fallback if direct fails)
cat local_file | ssh user@host 'cd /remote/path && /bin/iconv -f 1047 -t 819 > file'
```

**Why Different Approaches:**
- **Unix**: Tar is efficient for batch transfers, handles permissions well
- **z/OS**: Tar with iconv causes checksum corruption; individual file transfer is more reliable
- **Smart Fallback**: Some z/OS systems handle ASCII automatically, so try direct first for better performance

**z/OS Transfer Process:**
1. Read file content locally
2. Create remote directory if needed (`mkdir -p`)
3. Try direct transfer via `cat > file`
4. If direct fails, retry with `/bin/iconv -f 1047 -t 819 > file`
5. Track bytes transferred for statistics

## z/OS Specific Handling

### Detection
```bash
which /bin/iconv > /dev/null 2>&1 && echo ZOS || echo UNIX
```

**Why `/bin/iconv`:**
- Standard location on z/OS
- More reliable than checking for `aepipe` (external tool)
- Part of base z/OS installation

### EBCDIC/ASCII Conversion
**Codepages:**
- 1047: EBCDIC Latin-1/Open Systems (source)
- 819: ASCII ISO 8859-1 (target)

**Pipeline:**
```bash
tar -cf - files | ssh host 'iconv -f 1047 -t 819 | tar -xvfUX -'
```

**Why This Works:**
- `iconv` converts the tar stream byte-by-byte
- z/OS tar can read the converted ASCII stream
- No temporary files needed
- Handles binary data correctly

### Error Handling
**Common z/OS Error:**
```
tar: FSUM7171 filename: cannot set uid/gid: EDC5139I Operation not permitted.
```

**Solution:**
```go
if err != nil && isZOS {
    if strings.Contains(outputStr, "cannot set uid/gid") && 
       strings.Contains(outputStr, "x ") {
        // File extracted successfully, ignore ownership error
        return nil
    }
}
```

**Why This Works:**
- z/OS tar reports ownership errors even with `-o` flag
- Presence of "x " in output indicates successful extraction
- Ownership errors are non-fatal for development workflows

### EBCDIC Output Issues
**Problem:** Git commands on z/OS may output EBCDIC-encoded text when stderr is redirected.

**Solution:** Suppress stderr completely:
```bash
git checkout branch >/dev/null 2>/dev/null && echo 'OK' || echo 'FAILED'
```

## Branch Management

### Remote Branch Checkout
Function: `ensureRemoteBranch(remote *RemoteConfig, branch string) error`

**Process:**
1. Verify remote is a git repository
2. Attempt to checkout branch (suppress all output)
3. Check result via echo statement

**Why Not Fetch:**
- Fetching can be slow and may fail (network issues, corrupted refs)
- We're syncing working tree, not git history
- Branch will be correct after .git sync (if needed in future)
- Simpler and more reliable for development workflow

## Performance Optimizations

### Batch Operations
1. **Checksum Comparison:** All files checked in single SSH call
2. **File Transfer:** Up to 100 files per tar archive
3. **Remote Cleanup:** Up to 50 files per rm command (if implemented)

### Skip Unchanged Files
- Checksum comparison prevents unnecessary transfers
- Typical sync of unchanged repository: ~2.7s (vs ~5.3s with transfer)
- Saves bandwidth and time on large repositories

### Temporary Files
```go
tmpFile, err := os.CreateTemp("", "git-sync-*.txt")
defer os.Remove(tmpFile.Name())
```

**Why Temporary Files:**
- Avoids shell command line length limits
- Handles filenames with spaces/special characters
- Automatic cleanup via defer

## Error Handling Strategy

### Non-Fatal Errors
- z/OS ownership errors (file extracted successfully)
- Remote branch checkout failures (will be fixed by .git sync)
- Individual file checksum failures (file included in sync)

### Fatal Errors
- SSH connection failures
- Remote path doesn't exist
- Not in a git repository
- Configuration file missing/invalid

### Error Reporting
```go
type SyncStats struct {
    FilesTransferred int
    BytesTransferred int64
    Duration         time.Duration
    Errors           []string  // Non-fatal errors collected here
}
```

**Design Decision:** Continue syncing even if some files fail, report all errors at end.

## Security Considerations

### SSH Key Authentication
- Relies on SSH key-based authentication
- No password storage or prompting
- Uses user's existing SSH configuration

### Configuration File
- `._remote_sync` should be in `.gitignore`
- Contains connection information (not credentials)
- Should have restricted permissions (600)

### Command Injection Prevention
- Uses `exec.Command()` with separate arguments (not shell parsing)
- File lists passed via temporary files (not command line)
- No user input directly interpolated into shell commands

## Testing Strategy

### Manual Testing Checklist
1. **First Sync:** Verify files are transferred
2. **Second Sync:** Verify unchanged files are skipped
3. **Modified File:** Verify only changed file is synced
4. **Untracked File:** Verify untracked files are included
5. **z/OS Target:** Verify EBCDIC conversion works
6. **Remote Setup:** Verify environment commands execute

### Test Scenarios
```bash
# Test 1: Initial sync
echo "test" > testfile
git-remote-sync

# Test 2: No changes
git-remote-sync  # Should skip all files

# Test 3: Modified file
echo "modified" > testfile
git-remote-sync  # Should sync only testfile

# Test 4: Untracked file
echo "new" > newfile
git-remote-sync  # Should sync newfile
```

## Future Enhancement Ideas

### High Priority
1. **Parallel Transfers:** Use goroutines for concurrent file transfers
2. **Progress Bar:** Show transfer progress for large files
3. **Dry Run Mode:** `--dry-run` flag to preview changes
4. **Verbose Mode:** `--verbose` flag for detailed logging

### Medium Priority
5. **Compression:** Add gzip compression for large files
6. **Exclude Patterns:** Support `.syncignore` file
7. **Bidirectional Sync:** Detect and pull remote changes
8. **Delta Sync:** Use rsync algorithm for large files

### Low Priority
9. **Config Validation:** `--check-config` command
10. **Multiple Remotes:** Support syncing to multiple destinations
11. **Hooks:** Pre/post sync hook scripts
12. **Metrics:** Track bandwidth usage, transfer speeds

## Maintenance Guidelines

### Adding New Features
1. Update `RemoteConfig` struct if new config options needed
2. Add new functions following existing naming conventions
3. Update `SyncStats` if new metrics needed
4. Test on both Unix and z/OS systems
5. Update README.md and this document

### Debugging Tips
1. **SSH Issues:** Test SSH manually first: `ssh user@host 'echo OK'`
2. **Checksum Mismatches:** Compare `shasum` output manually
3. **z/OS Encoding:** Check for garbled output, suppress stderr
4. **Tar Errors:** Run tar commands manually to isolate issues

### Code Style
- Use descriptive function names
- Keep functions focused (single responsibility)
- Add comments for non-obvious logic
- Use `fmt.Errorf()` for error wrapping
- Prefer `strings.TrimSpace()` over manual trimming

### Dependencies
**Standard Library Only:**
- `bufio`: Scanning command output
- `bytes`: In-memory buffers
- `fmt`: Formatting and errors
- `os`: File operations
- `os/exec`: Running commands
- `path/filepath`: Path manipulation
- `strings`: String operations
- `time`: Duration tracking

**Why No External Dependencies:**
- Easier to build and distribute
- No dependency management issues
- Smaller binary size
- More portable

## Build and Distribution

### Building
```bash
go build -o git-remote-sync .
```

### Cross-Compilation
```bash
# For Linux
GOOS=linux GOARCH=amd64 go build -o git-remote-sync-linux .

# For macOS
GOOS=darwin GOARCH=amd64 go build -o git-remote-sync-macos .

# For z/OS (if Go supports it)
GOOS=zos GOARCH=s390x go build -o git-remote-sync-zos .
```

### Installation
```bash
# System-wide
sudo cp git-remote-sync /usr/local/bin/

# User-local
cp git-remote-sync ~/bin/
```

## Troubleshooting Common Issues

### Issue: "SSH connection failed"
**Cause:** SSH keys not set up or remote host unreachable
**Solution:** 
```bash
ssh-copy-id user@host
ssh user@host 'echo OK'
```

### Issue: "cannot set uid/gid" on z/OS
**Cause:** Normal z/OS behavior, not an actual error
**Solution:** Already handled in code, file is extracted successfully

### Issue: Garbled output from git commands
**Cause:** EBCDIC encoding in stderr on z/OS
**Solution:** Suppress stderr: `command 2>/dev/null`

### Issue: Files not syncing
**Cause:** Checksums match but files appear different
**Solution:** Check file encoding, line endings, or permissions

### Issue: "remote path is not a git repository"
**Cause:** Remote path doesn't exist or isn't initialized
**Solution:**
```bash
ssh user@host 'cd /path && git init'
```

## Performance Benchmarks

### Typical Performance (100 files, ~1MB total)
- **First sync:** 5-6 seconds
- **No changes:** 2-3 seconds
- **1 file changed:** 3-4 seconds

### Bottlenecks
1. **SSH Latency:** Each SSH call adds ~100-200ms
2. **Checksum Calculation:** ~10ms per file locally
3. **Tar Creation:** ~50ms for 100 files
4. **Network Transfer:** Depends on bandwidth

### Optimization Opportunities
- Reduce SSH calls (batch operations)
- Parallel checksum calculation
- Compression for large files
- Keep-alive SSH connections

## Version History

### v1.4.5 (Current)
- **Remote-only change revert** - Reverts tracked files modified on the remote that are clean locally
- Compares `git status --porcelain -z` maps from both sides to find remote-only changes
- Reverts only those files via `git checkout HEAD -- <files>` (shell-quoted, batched)
- Falls back to full `git reset --hard HEAD` if the targeted revert fails (e.g. staged-new files)
- Fixes sync failure (`git status mismatch`) when test runs on the remote dirty tracked files
- Preserves the checksum-skip optimization: files modified on both sides are untouched

### v1.4.4
- **Symlink sync** - Explicitly recreates all git-tracked symlinks on the remote after commit sync
- Fixes missing/broken symlinks on z/OS where `git reset --hard` does not restore mode `120000` entries
- Uses a single SSH round-trip for all symlinks (`rm -f` + `ln -sf` in batch)
- New function: `syncSymlinks()`, called from `syncRepository()` between commit sync and file sync

### v1.4.3
- **Fixed deleted file handling** - Skip deleted files in getFilesToSync to prevent sync errors
- **Improved error handling** - No longer attempts to read files marked as deleted by git
- **Better status parsing** - Extracts and checks git status codes before processing files
- Fixes "failed to read: no such file or directory" errors for deleted files
- Prevents unnecessary error messages in sync summary

### v1.4.2
- **Hostname-only format support** - Now accepts `hostname` without path for SSH config entries
- **Flexible remote-path parsing** - Supports `user@host:/path`, `host:/path`, `user@host`, and `host`
- **SSH config integration** - Works seamlessly with SSH config entries that define default user/path
- **Improved validation** - Only requires hostname, path is optional
- **Better error messages** - Clear feedback for configuration issues
- Fixes "invalid remote-path format" error when using SSH config entries
- Allows using SSH config defaults for user and working directory

### v1.4.1
- **Fixed z/OS file sync bug** - Modified files now sync correctly to z/OS systems
- **Smart z/OS transfer** - Individual file transfer with automatic fallback (direct or iconv)
- **Improved reliability** - Replaced tar-based transfer that caused checksum corruption
- **Better performance** - Direct transfer tried first, iconv only as fallback
- **Lenient verification** - Accepts synced files even if git status differs temporarily
- **Enhanced version output** - `--version` now shows commit hash and date for debugging
- Fixes "checksum error on tape" issue on z/OS systems
- Maintains efficient tar-based batch transfer for Unix systems

### v1.4.0
- **Automatic push detection** - Detects unpushed local commits automatically
- **Safe push validation** - Uses `git merge-base --is-ancestor` to ensure fast-forward
- **Auto-push to origin** - Pushes commits to origin if safe (no force push)
- **Remote fetch after push** - Triggers remote to fetch newly pushed commits
- **Divergence protection** - Fails with clear error if force push would be required
- **No origin handling** - Gracefully skips push check if no origin configured
- Fixes issue where local-only commits caused sync failures
- Eliminates need for manual `git push` before sync

### v1.3.0
- **Commit-level synchronization** - Direct ref update to sync commits
- **Optimized network traffic** - Skips sync when already at same commit (zero traffic)
- **Verbose mode (-v)** - Shows all git commands with L: (local) or R: (remote) prefixes
- Uses `git update-ref` + `git reset --hard` on remote (avoids bundle pack corruption)
- Assumes remote has objects from previous syncs or git fetch
- Ensures remote repository is at the exact same commit as local
- Fixes issue where branches were on same name but different commits
- Version flag changed to `--version` (was `-v`)

### v1.2.0
- Git status verification after sync
- Automatic cleanup of deleted files
- Special character support in filenames (using git -z flag)
- Force checkout with `git checkout -f` to clean working directory
- Null-terminated git output parsing for robust filename handling

### v1.1.0
- Command-line flag support (`-remote-path`, `-remote-setup`)
- Config file now optional (can use flags only)
- Flags override config file settings
- Enhanced help documentation with examples

### v1.0.0
- Initial release
- Basic file synchronization
- z/OS support with iconv
- Checksum-based skip logic
- Remote environment setup
- Config file based configuration

### Planned for Future Versions
- Parallel transfers
- Progress indicators
- Dry run mode
- Better error messages

## Contributing Guidelines

### Code Review Checklist
- [ ] Follows existing code style
- [ ] Includes error handling
- [ ] Works on both Unix and z/OS
- [ ] No external dependencies added
- [ ] Documentation updated
- [ ] Tested manually

### Testing Requirements
- Test on Linux/macOS
- Test on z/OS if changes affect encoding
- Test with various file types
- Test error conditions

## License and Credits

**License:** MIT License

**Author:** Created for syncing Go development repositories to z/OS systems

**Acknowledgments:**
- Uses standard Go libraries only
- Inspired by rsync but simplified for git workflows
- z/OS support based on IBM documentation
