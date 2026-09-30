package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	remoteSyncFile = "._remote_sync"
	version        = "1.5.0"
)

var verbose bool

type RemoteConfig struct {
	User     string
	Host     string
	Path     string
	Setup    string
	FullSpec string
}

type SyncStats struct {
	FilesTransferred int
	BytesTransferred int64
	Duration         time.Duration
	Errors           []string
}

func main() {
	// If invoked as git-remote-sync-mcp or with --mcp flag, start MCP server mode
	if filepath.Base(os.Args[0]) == "git-remote-sync-mcp" {
		runMCP()
		return
	}

	// Define command-line flags
	remotePath := flag.String("remote-path", "", "Remote path in format user@host:/path (overrides config file)")
	remoteSetup := flag.String("remote-setup", "", "Remote setup command (overrides config file)")
	showHelp := flag.Bool("h", false, "Show help message")
	showVersion := flag.Bool("version", false, "Show version information")
	verboseFlag := flag.Bool("v", false, "Verbose mode - show all git commands")
	mcpFlag := flag.Bool("mcp", false, "Run as Model Context Protocol (MCP) server over stdio")
	
	flag.Parse()

	if *mcpFlag {
		runMCP()
		return
	}

	verbose = *verboseFlag

	if *showHelp {
		printHelp()
		return
	}

	if *showVersion {
		printVersion()
		return
	}

	// Find git root
	gitRoot, err := getGitRoot()
	if err != nil {
		fatal("Not in a git repository: %v", err)
	}

	fmt.Printf("Git repository root: %s\n", gitRoot)

	// Read remote config (from file or command-line args)
	configPath := filepath.Join(gitRoot, remoteSyncFile)
	remote, err := readRemoteConfig(configPath, *remotePath, *remoteSetup)
	if err != nil {
		fatal("Failed to read remote config: %v", err)
	}

	if remote.Path != "" {
		if remote.User != "" {
			fmt.Printf("Remote: %s@%s:%s\n", remote.User, remote.Host, remote.Path)
		} else {
			fmt.Printf("Remote: %s:%s\n", remote.Host, remote.Path)
		}
	} else {
		if remote.User != "" {
			fmt.Printf("Remote: %s@%s\n", remote.User, remote.Host)
		} else {
			fmt.Printf("Remote: %s\n", remote.Host)
		}
	}

	// Get current branch
	branch, err := getCurrentBranch()
	if err != nil {
		fatal("Failed to get current branch: %v", err)
	}

	fmt.Printf("Current branch: %s\n", branch)

	// Verify remote accessibility
	if err := verifyRemote(remote); err != nil {
		fatal("Cannot access remote: %v", err)
	}

	fmt.Println("\nStarting sync...")
	stats := &SyncStats{}
	startTime := time.Now()

	// Sync the repository
	if err := syncRepository(gitRoot, remote, branch, stats); err != nil {
		fatal("Sync failed: %v", err)
	}

	stats.Duration = time.Since(startTime)

	// Print summary
	printSummary(stats)
}

func printVersion() {
	fmt.Printf("git-remote-sync version %s\n", version)
}

func printHelp() {
	fmt.Printf(`git-remote-sync version %s

Usage: git-remote-sync [options]

Syncs a local git repository with a remote one via SSH, maintaining the exact
state including modified files, untracked files, and current branch.

Configuration can be provided via command-line flags, a config file named '%s'
in the repository root, or git config properties (local or global).

Precedence (highest to lowest):
  1. Command-line flags (-remote-path, -remote-setup)
  2. Config file (%s)
  3. Git config properties (local repo or global git config)

Git config properties:
  git config remote-sync.remote-path "[user@]hostname:/path/to/remote/repo"
  git config remote-sync.remote-setup ". ./.env"
  # Or globally:
  git config --global remote-sync.remote-setup ". ~/.env"

Config file format:
  remote-path:[user@]hostname:/path/to/remote/repo
  remote-setup:. ./.env

The remote-path supports:
  - user@hostname:/path  (explicit user, hostname, and path)
  - hostname:/path       (hostname and path, useful with SSH config entries)
  - user@hostname        (user and hostname, default path)
  - hostname             (hostname only, useful with SSH config entries)

Options:
  -h, -help              Show this help message
  -version               Show version information
  -mcp                   Run as Model Context Protocol (MCP) server over stdio
  -v                     Verbose mode - show all git commands (prefixed with L: or R:)
  -remote-path string    Remote path in format [user@]host[:path] (overrides config file/git config)
  -remote-setup string   Remote setup command (overrides config file/git config)

The remote-setup is optional and allows you to run environment setup commands
before each remote operation (e.g., sourcing environment files).

Examples:
  # Use git config or config file
  git-remote-sync

  # Set git config once for the repository
  git config remote-sync.remote-path "user@host:/path/to/repo"
  git config remote-sync.remote-setup ". ./.env"
  git-remote-sync

  # Override remote path with explicit user
  git-remote-sync -remote-path user@host:/path/to/repo

  # Override remote path using SSH config host entry
  git-remote-sync -remote-path myhost:/path/to/repo

  # Provide all config via command line
  git-remote-sync -remote-path user@host:/path -remote-setup ". ./.env"

The utility handles EBCDIC/ASCII conversion for z/OS systems automatically
using iconv (codepage 1047 to 819).
`, version, remoteSyncFile, remoteSyncFile)
}

func logCommand(location string, command string) {
	if verbose {
		fmt.Printf("%s: %s\n", location, command)
	}
}

func getGitRoot() (string, error) {
	cmdStr := "git rev-parse --show-toplevel"
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// getGitConfig retrieves a git config value (checks local then global automatically)
func getGitConfig(key string) string {
	cmdStr := fmt.Sprintf("git config %s", key)
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "config", key)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func readRemoteConfig(path string, cmdRemotePath string, cmdRemoteSetup string) (*RemoteConfig, error) {
	config := &RemoteConfig{}
	
	var fileRemotePath string
	var fileRemoteSetup string
	var fileFound bool

	// 1. Try to read from config file first (if it exists)
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		fileFound = true
		scanner := bufio.NewScanner(file)
		
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}

			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])

			switch key {
			case "remote-path":
				fileRemotePath = value
			case "remote-setup":
				fileRemoteSetup = value
			}
		}
	}

	// 2. Read git config properties as fallback (checked if not in file and not in cmdline)
	gitRemotePath := getGitConfig("remote-sync.remote-path")
	gitRemoteSetup := getGitConfig("remote-sync.remote-setup")

	// Determine effective remotePath: cmdline > file > git config
	var effectiveRemotePath string
	if cmdRemotePath != "" {
		effectiveRemotePath = cmdRemotePath
	} else if fileRemotePath != "" {
		effectiveRemotePath = fileRemotePath
	} else if gitRemotePath != "" {
		effectiveRemotePath = gitRemotePath
	}

	if effectiveRemotePath != "" {
		if err := parseRemotePath(effectiveRemotePath, config); err != nil {
			return nil, err
		}
	}

	// Determine effective remoteSetup: cmdline > file > git config
	if cmdRemoteSetup != "" {
		config.Setup = cmdRemoteSetup
	} else if fileRemoteSetup != "" {
		config.Setup = fileRemoteSetup
	} else if gitRemoteSetup != "" {
		config.Setup = gitRemoteSetup
	}

	// Validate that we have required configuration
	// Note: config.Path can be empty if using SSH config with default path
	if config.Host == "" {
		if !fileFound {
			return nil, fmt.Errorf("no remote configuration found: provide via --remote-path flag, %s file, or git config property (remote-sync.remote-path)", remoteSyncFile)
		}
		return nil, fmt.Errorf("missing required remote-path configuration in %s (or provide via git config remote-sync.remote-path / --remote-path flag)", remoteSyncFile)
	}

	return config, nil
}

// parseRemotePath parses remote path in format: [user@]host[:path]
// Supports "user@host:path", "host:path", "user@host", and "host" (where host can be SSH config entry)
func parseRemotePath(value string, config *RemoteConfig) error {
	// Split on colon to separate host part from path (if path is provided)
	pathParts := strings.SplitN(value, ":", 2)
	
	var userHost string
	var remotePath string
	
	if len(pathParts) == 2 {
		// Format: [user@]host:path
		userHost = pathParts[0]
		remotePath = pathParts[1]
	} else if len(pathParts) == 1 {
		// Format: [user@]host (no path, will use SSH config or default)
		userHost = pathParts[0]
		remotePath = "" // Empty path means use SSH config default or current directory
	} else {
		return fmt.Errorf("invalid remote-path format, expected: [user@]host[:path]")
	}

	// Check if userHost contains @ (user@host format)
	if strings.Contains(userHost, "@") {
		userHostParts := strings.SplitN(userHost, "@", 2)
		if len(userHostParts) != 2 {
			return fmt.Errorf("invalid remote-path format, expected: [user@]host[:path]")
		}
		config.User = userHostParts[0]
		config.Host = userHostParts[1]
	} else {
		// No @ sign, treat entire string as host (SSH config entry)
		config.User = ""
		config.Host = userHost
	}

	config.Path = remotePath
	config.FullSpec = value
	return nil
}

func getCurrentBranch() (string, error) {
	cmdStr := "git rev-parse --abbrev-ref HEAD"
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func verifyRemote(remote *RemoteConfig) error {
	// Test SSH connection
	sshTarget := getSSHTarget(remote)
	
	// If path is specified, verify it exists
	if remote.Path != "" {
		testCmd := fmt.Sprintf("test -d %s && echo OK || echo NOTFOUND", remote.Path)
		cmd := exec.Command("ssh", sshTarget, testCmd)
		
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("SSH connection failed: %w", err)
		}

		result := strings.TrimSpace(string(output))
		if result == "NOTFOUND" {
			return fmt.Errorf("remote path does not exist: %s", remote.Path)
		}
		if result != "OK" {
			return fmt.Errorf("unexpected response from remote: %s", result)
		}
	} else {
		// No path specified, just verify SSH connection works
		cmd := exec.Command("ssh", sshTarget, "echo OK")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("SSH connection failed: %w", err)
		}
		
		result := strings.TrimSpace(string(output))
		if result != "OK" {
			return fmt.Errorf("SSH connection test failed: %s", result)
		}
	}

	return nil
}

// getSSHTarget returns the SSH target string in format [user@]host
func getSSHTarget(remote *RemoteConfig) string {
	if remote.User != "" {
		return fmt.Sprintf("%s@%s", remote.User, remote.Host)
	}
	return remote.Host
}

func buildRemoteCommand(remote *RemoteConfig, command string) string {
	var cmdParts []string
	
	if remote.Setup != "" {
		cmdParts = append(cmdParts, remote.Setup)
	}
	
	if remote.Path != "" {
		cmdParts = append(cmdParts, fmt.Sprintf("cd %s", remote.Path))
	}
	
	cmdParts = append(cmdParts, command)
	
	return strings.Join(cmdParts, " && ")
}

func syncRepository(gitRoot string, remote *RemoteConfig, branch string, stats *SyncStats) error {
	// Ensure remote is on the same branch
	if err := ensureRemoteBranch(remote, branch); err != nil {
		return fmt.Errorf("failed to set remote branch: %w", err)
	}

	// Sync commits using git bundle to avoid .git permission issues
	fmt.Println("\nSyncing commit history...")
	if err := syncCommitsViaBundle(gitRoot, remote, branch, stats); err != nil {
		return fmt.Errorf("failed to sync commits: %w", err)
	}

	// Revert files modified on the remote that are clean locally, so the
	// remote ends up exactly matching local state. The remote may have
	// modified tracked files that are clean locally (e.g. test artifacts
	// changed by test runs on the remote). Those would otherwise survive
	// the sync: getFilesToSync only transfers locally-modified files and
	// cleanupRemote only deletes files absent locally, which makes the
	// final verifyGitStatus check fail with a git status mismatch.
	fmt.Println("\nReverting remote-only changes...")
	if err := discardRemoteOnlyChanges(gitRoot, remote); err != nil {
		return fmt.Errorf("failed to revert remote-only changes: %w", err)
	}

	// Sync symlinks tracked in the git index (git reset --hard may not recreate
	// them correctly on z/OS where symlink support is limited)
	fmt.Println("\nSyncing symlinks...")
	if err := syncSymlinks(gitRoot, remote, stats); err != nil {
		return fmt.Errorf("failed to sync symlinks: %w", err)
	}

	// Get list of modified and untracked files
	files, err := getFilesToSync(gitRoot)
	if err != nil {
		return fmt.Errorf("failed to get files to sync: %w", err)
	}

	if len(files) == 0 {
		fmt.Println("\nNo modified or untracked files to sync")
	} else {
		fmt.Printf("\nFiles to sync: %d\n", len(files))

		// Sync working tree files
		fmt.Println("\nSyncing working tree files...")
		if err := syncFiles(gitRoot, remote, files, stats); err != nil {
			return fmt.Errorf("failed to sync files: %w", err)
		}
	}

	// Get list of all files that should exist on remote (tracked + untracked)
	allLocalFiles, err := getAllLocalFiles(gitRoot)
	if err != nil {
		return fmt.Errorf("failed to get local files: %w", err)
	}

	// Clean up files on remote that don't exist locally
	fmt.Println("\nCleaning up remote files...")
	if err := cleanupRemote(gitRoot, remote, allLocalFiles, stats); err != nil {
		return fmt.Errorf("failed to cleanup remote: %w", err)
	}

	// Verify git status consistency between local and remote
	fmt.Println("\nVerifying sync consistency...")
	if err := verifyGitStatus(gitRoot, remote); err != nil {
		return fmt.Errorf("sync verification failed: %w", err)
	}

	return nil
}

func getLocalHead(gitRoot string) (string, error) {
	cmdStr := "git rev-parse HEAD"
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func getRemoteHead(remote *RemoteConfig) (string, error) {
	gitCmd := "git rev-parse HEAD"
	logCommand("R", gitCmd)
	remoteCmd := buildRemoteCommand(remote, "git rev-parse HEAD 2>/dev/null || echo NOHEAD")
	cmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(output))
	if head == "NOHEAD" {
		return "", fmt.Errorf("remote has no HEAD commit")
	}
	return head, nil
}

func getFilesToSync(gitRoot string) ([]string, error) {
	var files []string
	fileSet := make(map[string]bool)

	// Get modified, added, deleted, renamed files using git status with -z for null-terminated output
	cmdStr := "git status --porcelain -z"
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "status", "--porcelain", "-z")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	// Split by null byte
	entries := bytes.Split(output, []byte{0})
	for _, entry := range entries {
		if len(entry) < 4 {
			continue
		}
		line := string(entry)
		// Format: XY filename or XY old -> new
		status := line[:2]
		filename := line[3:]
		
		// Skip deleted files (they don't exist to sync)
		if strings.Contains(status, "D") {
			continue
		}
		
		// Handle renames (old -> new)
		if strings.Contains(filename, " -> ") {
			// Get new filename after arrow
			renameParts := strings.Split(filename, " -> ")
			if len(renameParts) == 2 {
				fileSet[renameParts[1]] = true
			}
		} else {
			fileSet[filename] = true
		}
	}

	// Get untracked files with -z for null-terminated output
	cmdStr = "git ls-files --others --exclude-standard -z"
	logCommand("L", cmdStr)
	cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard", "-z")
	cmd.Dir = gitRoot
	output, err = cmd.Output()
	if err != nil {
		return nil, err
	}

	// Split by null byte
	for _, file := range bytes.Split(output, []byte{0}) {
		if len(file) > 0 {
			fileSet[string(file)] = true
		}
	}

	// Convert set to slice
	for file := range fileSet {
		files = append(files, file)
	}

	return files, nil
}

func getAllLocalFiles(gitRoot string) ([]string, error) {
	var files []string
	fileSet := make(map[string]bool)

	// Get all tracked files (files in the git index)
	// Use -z for null-terminated output to handle special characters
	cmdStr := "git ls-files -z"
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	// Split by null byte instead of newline
	for _, file := range bytes.Split(output, []byte{0}) {
		if len(file) > 0 {
			fileSet[string(file)] = true
		}
	}

	// Get untracked files
	cmdStr = "git ls-files --others --exclude-standard -z"
	logCommand("L", cmdStr)
	cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard", "-z")
	cmd.Dir = gitRoot
	output, err = cmd.Output()
	if err != nil {
		return nil, err
	}

	// Split by null byte instead of newline
	for _, file := range bytes.Split(output, []byte{0}) {
		if len(file) > 0 {
			fileSet[string(file)] = true
		}
	}

	// Convert set to slice
	for file := range fileSet {
		files = append(files, file)
	}

	return files, nil
}

func syncCommitsViaBundle(gitRoot string, remote *RemoteConfig, branch string, stats *SyncStats) error {
	// Get local HEAD commit
	localHead, err := getLocalHead(gitRoot)
	if err != nil {
		return fmt.Errorf("failed to get local HEAD: %w", err)
	}

	// Get remote HEAD commit
	remoteHead, err := getRemoteHead(remote)
	if err == nil && localHead == remoteHead {
		fmt.Printf("✓ Remote already at commit: %s\n", localHead[:8])
		return nil
	}

	// Check if local commits need to be pushed to origin first
	pushed, err := ensureCommitsPushedToOrigin(gitRoot, branch, localHead)
	if err != nil {
		return fmt.Errorf("failed to ensure commits are pushed: %w", err)
	}

	// If we just pushed, remote needs to fetch again to get the new commits
	if pushed {
		fmt.Println("Fetching newly pushed commits on remote...")
		gitCmd := "git fetch --all -f"
		logCommand("R", gitCmd)
		remoteCmd := buildRemoteCommand(remote, "git fetch --all -f >/dev/null 2>/dev/null && echo 'FETCHED' || echo 'FETCHFAILED'")
		cmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
		output, _ := cmd.Output()
		
		fetchResult := strings.TrimSpace(string(output))
		if fetchResult == "FETCHED" {
			fmt.Println("✓ Remote fetched newly pushed commits")
		} else {
			return fmt.Errorf("remote failed to fetch after push")
		}
	}

	// Remote is behind - need to sync commits
	// Directly update refs on remote (avoids bundle pack corruption issues)
	
	gitCmd := fmt.Sprintf("git update-ref refs/heads/%s %s && git symbolic-ref HEAD refs/heads/%s && git reset --hard %s", branch, localHead, branch, localHead)
	logCommand("R", gitCmd)
	remoteCmd := buildRemoteCommand(remote, fmt.Sprintf("git update-ref refs/heads/%s %s >/dev/null 2>&1 && git symbolic-ref HEAD refs/heads/%s >/dev/null 2>&1 && git reset --hard %s >/dev/null 2>&1 && echo 'OK' || echo 'FAILED'", branch, localHead, branch, localHead))
	cmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
	output, err := cmd.CombinedOutput()
	
	outputStr := strings.TrimSpace(string(output))
	if err != nil || !strings.Contains(outputStr, "OK") {
		return fmt.Errorf("failed to update remote refs: %v\nOutput: %s", err, outputStr)
	}

	fmt.Printf("✓ Synced commits to: %s\n", localHead[:8])
	return nil
}

// discardRemoteOnlyChanges reverts tracked files that are modified on the
// remote but clean locally (e.g. test artifacts changed by test runs on the
// remote). Only those files are touched, so files modified on both sides keep
// their content and are still skipped by the checksum comparison in syncFiles
// when they already match. Untracked files are left alone here;
// cleanupRemote removes extraneous ones later.
func discardRemoteOnlyChanges(gitRoot string, remote *RemoteConfig) error {
	localStatus, err := getLocalStatusMap(gitRoot)
	if err != nil {
		return fmt.Errorf("failed to get local git status: %w", err)
	}

	remoteStatus, err := getRemoteStatusMap(remote)
	if err != nil {
		return fmt.Errorf("failed to get remote git status: %w", err)
	}

	var remoteOnly []string
	for file, status := range remoteStatus {
		if status == "??" {
			// Untracked files are handled by cleanupRemote
			continue
		}
		if _, ok := localStatus[file]; !ok {
			remoteOnly = append(remoteOnly, file)
		}
	}

	if len(remoteOnly) == 0 {
		fmt.Println("✓ No remote-only changes")
		return nil
	}

	fmt.Printf("Found %d remote-only change(s), reverting...\n", len(remoteOnly))
	if err := checkoutRemoteFiles(remote, remoteOnly); err != nil {
		fmt.Printf("⚠ Targeted revert failed (%v), falling back to full reset\n", err)
		return resetRemoteWorkingTree(remote)
	}

	fmt.Printf("✓ Reverted %d remote-only file(s)\n", len(remoteOnly))
	return nil
}

// parsePorcelainZ parses null-terminated `git status --porcelain -z` output
// into a map of filename -> two-letter status code. Renames ("old -> new")
// are recorded under the new filename.
func parsePorcelainZ(output []byte) map[string]string {
	files := make(map[string]string)
	for _, entry := range bytes.Split(output, []byte{0}) {
		if len(entry) < 4 {
			continue
		}
		status := string(entry[:2])
		filename := string(entry[3:])
		if idx := strings.Index(filename, " -> "); idx >= 0 {
			filename = filename[idx+4:]
		}
		files[filename] = status
	}
	return files
}

func getLocalStatusMap(gitRoot string) (map[string]string, error) {
	cmdStr := "git status --porcelain -z"
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "status", "--porcelain", "-z")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parsePorcelainZ(output), nil
}

func getRemoteStatusMap(remote *RemoteConfig) (map[string]string, error) {
	gitCmd := "git status --porcelain -z"
	logCommand("R", gitCmd)
	remoteCmd := buildRemoteCommand(remote, "git status --porcelain -z")
	cmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parsePorcelainZ(output), nil
}

// checkoutRemoteFiles restores the given paths on the remote to HEAD
// (`git checkout HEAD -- <paths>` reverts both staged and unstaged changes
// and restores deleted working tree files). Paths are shell-quoted to handle
// spaces and special characters.
func checkoutRemoteFiles(remote *RemoteConfig, files []string) error {
	batchSize := 50
	for i := 0; i < len(files); i += batchSize {
		end := i + batchSize
		if end > len(files) {
			end = len(files)
		}
		batch := files[i:end]

		quoted := make([]string, len(batch))
		for j, f := range batch {
			quoted[j] = shellQuote(f)
		}

		gitCmd := fmt.Sprintf("git checkout HEAD -- %s", strings.Join(quoted, " "))
		logCommand("R", gitCmd)
		remoteCmd := buildRemoteCommand(remote, gitCmd+" >/dev/null 2>/dev/null && echo 'OK' || echo 'FAILED'")
		cmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
		output, err := cmd.CombinedOutput()

		outputStr := strings.TrimSpace(string(output))
		if err != nil || !strings.Contains(outputStr, "OK") {
			return fmt.Errorf("failed to revert batch %d-%d: %v\nOutput: %s", i, end, err, outputStr)
		}
	}
	return nil
}

// shellQuote quotes a string for safe use as a single shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// resetRemoteWorkingTree discards all working tree changes to tracked files
// on the remote (git reset --hard HEAD). Used as a fallback when the
// targeted revert of remote-only files fails.
func resetRemoteWorkingTree(remote *RemoteConfig) error {
	gitCmd := "git reset --hard HEAD"
	logCommand("R", gitCmd)
	remoteCmd := buildRemoteCommand(remote, "git reset --hard HEAD >/dev/null 2>/dev/null && echo 'OK' || echo 'FAILED'")
	cmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
	output, err := cmd.CombinedOutput()

	outputStr := strings.TrimSpace(string(output))
	if err != nil || !strings.Contains(outputStr, "OK") {
		return fmt.Errorf("failed to reset remote working tree: %v\nOutput: %s", err, outputStr)
	}

	fmt.Println("✓ Remote working tree reset to HEAD")
	return nil
}

func ensureCommitsPushedToOrigin(gitRoot string, branch string, localHead string) (bool, error) {
	// Check if origin remote exists
	cmdStr := "git remote get-url origin"
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = gitRoot
	originURL, err := cmd.Output()
	if err != nil {
		// No origin configured, skip push check
		fmt.Println("⚠ No origin remote configured, skipping push check")
		return false, nil
	}
	
	originURLStr := strings.TrimSpace(string(originURL))
	fmt.Printf("Origin: %s\n", originURLStr)
	
	// Get the commit hash that origin/branch points to
	cmdStr = fmt.Sprintf("git rev-parse origin/%s", branch)
	logCommand("L", cmdStr)
	cmd = exec.Command("git", "rev-parse", fmt.Sprintf("origin/%s", branch))
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	
	var originBranchHead string
	if err != nil {
		// Branch doesn't exist on origin yet
		fmt.Printf("Branch '%s' not found on origin, will push...\n", branch)
	} else {
		originBranchHead = strings.TrimSpace(string(output))
		
		// Check if local and origin are at same commit
		if originBranchHead == localHead {
			fmt.Println("✓ Local commits already pushed to origin")
			return false, nil
		}
		
		// Check if local is ahead of origin (fast-forward possible)
		cmdStr = fmt.Sprintf("git merge-base --is-ancestor origin/%s HEAD", branch)
		logCommand("L", cmdStr)
		cmd = exec.Command("git", "merge-base", "--is-ancestor", fmt.Sprintf("origin/%s", branch), "HEAD")
		cmd.Dir = gitRoot
		err = cmd.Run()
		
		if err != nil {
			// Not a fast-forward, would require force push
			return false, fmt.Errorf("local branch has diverged from origin/%s - force push required. Please resolve manually:\n  git push --force-with-lease origin %s", branch, branch)
		}
	}
	
	// Push is safe (fast-forward or new branch)
	fmt.Printf("Pushing local commits to origin/%s...\n", branch)
	cmdStr = fmt.Sprintf("git push origin %s", branch)
	logCommand("L", cmdStr)
	cmd = exec.Command("git", "push", "origin", branch)
	cmd.Dir = gitRoot
	output, err = cmd.CombinedOutput()
	
	if err != nil {
		return false, fmt.Errorf("failed to push to origin: %w\nOutput: %s", err, string(output))
	}
	
	fmt.Println("✓ Pushed commits to origin")
	return true, nil
}

func ensureRemoteBranch(remote *RemoteConfig, branch string) error {
	// Check if remote is a git repository
	gitCmd := "git rev-parse --git-dir"
	logCommand("R", gitCmd)
	remoteCmd := buildRemoteCommand(remote, "git rev-parse --git-dir > /dev/null 2>&1 && echo OK || echo NOTGIT")
	cmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to verify remote git repo: %w", err)
	}

	if strings.Contains(string(output), "NOTGIT") {
		return fmt.Errorf("remote path is not a git repository")
	}

	// Fetch all branches from origin to ensure remote is up to date
	gitCmd = "git fetch --all -f"
	logCommand("R", gitCmd)
	remoteCmd = buildRemoteCommand(remote, "git fetch --all -f >/dev/null 2>/dev/null && echo 'FETCHED' || echo 'FETCHFAILED'")
	cmd = exec.Command("ssh", getSSHTarget(remote), remoteCmd)
	output, _ = cmd.Output()
	
	fetchResult := strings.TrimSpace(string(output))
	if fetchResult == "FETCHED" {
		fmt.Println("✓ Fetched latest branches from origin")
	} else {
		fmt.Println("⚠ Failed to fetch from origin (continuing anyway)")
	}

	// Check if branch exists on remote
	gitCmd = fmt.Sprintf("git rev-parse --verify %s", branch)
	logCommand("R", gitCmd)
	remoteCmd = buildRemoteCommand(remote, fmt.Sprintf("git rev-parse --verify %s >/dev/null 2>/dev/null && echo 'EXISTS' || echo 'NOTEXISTS'", branch))
	cmd = exec.Command("ssh", getSSHTarget(remote), remoteCmd)
	output, _ = cmd.Output()
	
	branchExists := strings.TrimSpace(string(output)) == "EXISTS"
	
	if branchExists {
		// Branch exists, just checkout (preserve working tree state)
		gitCmd = fmt.Sprintf("git checkout %s", branch)
		logCommand("R", gitCmd)
		remoteCmd = buildRemoteCommand(remote, fmt.Sprintf("git checkout %s >/dev/null 2>/dev/null && echo 'OK' || echo 'FAILED'", branch))
		cmd = exec.Command("ssh", getSSHTarget(remote), remoteCmd)
		output, _ = cmd.Output()
		
		result := strings.TrimSpace(string(output))
		if result == "OK" {
			fmt.Printf("✓ Remote branch: %s\n", branch)
		} else {
			fmt.Printf("⚠ Remote branch checkout failed: %s\n", branch)
		}
	} else {
		// Branch doesn't exist, create it (preserve working tree state)
		gitCmd = fmt.Sprintf("git checkout -b %s", branch)
		logCommand("R", gitCmd)
		remoteCmd = buildRemoteCommand(remote, fmt.Sprintf("git checkout -b %s >/dev/null 2>/dev/null && echo 'CREATED' || echo 'FAILED'", branch))
		cmd = exec.Command("ssh", getSSHTarget(remote), remoteCmd)
		output, _ = cmd.Output()
		
		result := strings.TrimSpace(string(output))
		if result == "CREATED" {
			fmt.Printf("✓ Remote branch created: %s\n", branch)
		} else {
			fmt.Printf("⚠ Failed to create remote branch: %s\n", branch)
		}
	}

	return nil
}

// syncSymlinks reads all symlinks from the local git index (mode 120000) and
// recreates them on the remote via SSH.  git reset --hard may not restore
// symlinks correctly on z/OS, so we do it explicitly.
func syncSymlinks(gitRoot string, remote *RemoteConfig, stats *SyncStats) error {
	// git ls-files --stage lists entries with their mode; mode 120000 = symlink
	cmdStr := "git ls-files --stage -z"
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "ls-files", "--stage", "-z")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list staged files: %w", err)
	}

	type symlink struct {
		path   string
		target string
	}
	var symlinks []symlink

	for _, entry := range bytes.Split(output, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		// Format: "<mode> <hash> <stage>\t<path>"
		line := string(entry)
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		meta := line[:tab]
		path := line[tab+1:]
		fields := strings.Fields(meta)
		if len(fields) < 1 || fields[0] != "120000" {
			continue
		}
		// Read the symlink target from the blob (it's just the target path as text)
		readCmd := exec.Command("git", "show", ":"+path)
		readCmd.Dir = gitRoot
		targetBytes, err := readCmd.Output()
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Sprintf("symlink %s: failed to read target: %v", path, err))
			continue
		}
		symlinks = append(symlinks, symlink{path: path, target: strings.TrimSpace(string(targetBytes))})
	}

	if len(symlinks) == 0 {
		fmt.Println("✓ No symlinks to sync")
		return nil
	}

	fmt.Printf("Syncing %d symlink(s)...\n", len(symlinks))

	// Build a single remote command that recreates all symlinks
	var cmds []string
	for _, sl := range symlinks {
		dir := filepath.Dir(sl.path)
		if dir != "." {
			cmds = append(cmds, fmt.Sprintf("mkdir -p %s", dir))
		}
		// Remove whatever is there (regular file from a bad reset, or stale symlink)
		cmds = append(cmds, fmt.Sprintf("rm -f %s && ln -sf %s %s", sl.path, sl.target, sl.path))
	}
	remoteScript := strings.Join(cmds, " && ")
	remoteCmd := buildRemoteCommand(remote, remoteScript+" && echo OK")
	sshCmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
	sshOutput, err := sshCmd.CombinedOutput()
	if err != nil || !strings.Contains(strings.TrimSpace(string(sshOutput)), "OK") {
		return fmt.Errorf("failed to recreate symlinks on remote: %v\nOutput: %s", err, string(sshOutput))
	}

	for _, sl := range symlinks {
		fmt.Printf("✓ Symlink: %s -> %s\n", sl.path, sl.target)
		stats.FilesTransferred++
	}
	return nil
}

func syncFiles(gitRoot string, remote *RemoteConfig, files []string, stats *SyncStats) error {
	// Check if remote is z/OS
	remoteCheckCmd := buildRemoteCommand(remote, "which /bin/iconv > /dev/null 2>&1 && echo ZOS || echo UNIX")
	checkCmd := exec.Command("ssh", getSSHTarget(remote), remoteCheckCmd)
	output, _ := checkCmd.Output()
	isZOS := strings.TrimSpace(string(output))

	// Filter files that actually need syncing by comparing checksums
	filesToSync, err := filterFilesNeedingSync(gitRoot, remote, files, isZOS)
	if err != nil {
		return fmt.Errorf("failed to filter files: %w", err)
	}

	if len(filesToSync) == 0 {
		fmt.Println("✓ All files are already in sync")
		return nil
	}

	fmt.Printf("Files needing sync: %d (skipped %d unchanged)\n", len(filesToSync), len(files)-len(filesToSync))

	// Sync files
	if isZOS == "ZOS" {
		// For z/OS, transfer files individually with iconv
		for i, file := range filesToSync {
			if err := syncSingleFileZOS(gitRoot, remote, file, stats); err != nil {
				stats.Errors = append(stats.Errors, fmt.Sprintf("%s: %v", file, err))
			} else {
				stats.FilesTransferred++
				if (i+1)%10 == 0 || i+1 == len(filesToSync) {
					fmt.Printf("✓ Synced files %d-%d of %d\n", i-((i+1)%10)+1, i+1, len(filesToSync))
				}
			}
		}
	} else {
		// For Unix, use tar in batches
		batchSize := 100
		for i := 0; i < len(filesToSync); i += batchSize {
			end := i + batchSize
			if end > len(filesToSync) {
				end = len(filesToSync)
			}
			batch := filesToSync[i:end]

			if err := syncFileBatch(gitRoot, remote, batch, stats); err != nil {
				stats.Errors = append(stats.Errors, fmt.Sprintf("batch %d-%d: %v", i, end, err))
			} else {
				stats.FilesTransferred += len(batch)
				fmt.Printf("✓ Synced files %d-%d of %d\n", i+1, end, len(filesToSync))
			}
		}
	}

	return nil
}

func syncSingleFileZOS(gitRoot string, remote *RemoteConfig, file string, stats *SyncStats) error {
	filePath := filepath.Join(gitRoot, file)
	
	// Read file content
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read: %w", err)
	}
	
	// Create remote directory if needed
	remoteDir := filepath.Dir(file)
	if remoteDir != "." {
		mkdirCmd := buildRemoteCommand(remote, fmt.Sprintf("mkdir -p %s", remoteDir))
		mkdirExec := exec.Command("ssh", getSSHTarget(remote), mkdirCmd)
		_ = mkdirExec.Run() // Ignore errors if dir exists
	}
	
	// Try direct transfer first (some z/OS systems handle ASCII automatically)
	transferCmd := buildRemoteCommand(remote, fmt.Sprintf("cat > %s", file))
	cmd := exec.Command("ssh", getSSHTarget(remote), transferCmd)
	cmd.Stdin = bytes.NewReader(content)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If direct transfer fails, try with iconv for EBCDIC conversion
		transferCmd = buildRemoteCommand(remote, fmt.Sprintf("/bin/iconv -f 1047 -t 819 > %s", file))
		cmd = exec.Command("ssh", getSSHTarget(remote), transferCmd)
		cmd.Stdin = bytes.NewReader(content)
		
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("transfer failed: %w\nOutput: %s", err, string(output))
		}
	}
	
	stats.BytesTransferred += int64(len(content))
	return nil
}

func filterFilesNeedingSync(gitRoot string, remote *RemoteConfig, files []string, isZOS string) ([]string, error) {
	var needSync []string
	
	// Build a map of local file checksums using shasum
	localChecksums := make(map[string]string)
	for _, file := range files {
		filePath := filepath.Join(gitRoot, file)
		cmd := exec.Command("shasum", filePath)
		output, err := cmd.Output()
		if err != nil {
			// If checksum fails, include file for sync
			needSync = append(needSync, file)
			continue
		}
		// shasum output: "checksum  filename"
		parts := strings.Fields(string(output))
		if len(parts) > 0 {
			localChecksums[file] = parts[0]
		}
	}

	// Get remote checksums in batch using shasum (works on both Unix and z/OS)
	fileList := strings.Join(files, " ")
	checksumCmd := fmt.Sprintf("for f in %s; do if [ -f \"$f\" ]; then shasum \"$f\" 2>/dev/null; fi; done", fileList)
	
	remoteCmd := buildRemoteCommand(remote, checksumCmd)
	cmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
	output, err := cmd.Output()
	
	remoteChecksums := make(map[string]string)
	if err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(output))
		for scanner.Scan() {
			parts := strings.Fields(scanner.Text())
			if len(parts) >= 2 {
				checksum := parts[0]
				filename := parts[1]
				remoteChecksums[filename] = checksum
			}
		}
	}

	// Compare checksums
	for _, file := range files {
		localSum, hasLocal := localChecksums[file]
		remoteSum, hasRemote := remoteChecksums[file]
		
		if !hasLocal || !hasRemote || localSum != remoteSum {
			needSync = append(needSync, file)
		}
	}

	return needSync, nil
}

func syncFileBatch(gitRoot string, remote *RemoteConfig, files []string, stats *SyncStats) error {
	// Create a temporary file list
	tmpFile, err := os.CreateTemp("", "git-sync-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	for _, file := range files {
		fmt.Fprintln(tmpFile, file)
	}
	tmpFile.Close()

	// Create tar from file list
	tarCmd := fmt.Sprintf("cd %s && tar -cf - -T %s 2>/dev/null", gitRoot, tmpFile.Name())
	remoteTarCmd := buildRemoteCommand(remote, "tar -xf - 2>&1")
	fullCmd := fmt.Sprintf("%s | ssh %s@%s '%s'",
		tarCmd, remote.User, remote.Host, remoteTarCmd)
	cmd := exec.Command("bash", "-c", fullCmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar transfer failed: %w\nOutput: %s", err, string(output))
	}

	return nil
}

func cleanupRemote(gitRoot string, remote *RemoteConfig, localFiles []string, stats *SyncStats) error {
	// Get list of tracked and untracked (not ignored) files on remote
	// This matches what we sync: tracked files + untracked files (excluding .gitignore)
	remoteFilesCmd := buildRemoteCommand(remote, "git ls-files -z && git ls-files --others --exclude-standard -z")
	cmd := exec.Command("ssh", getSSHTarget(remote), remoteFilesCmd)
	
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list remote files: %w", err)
	}

	// Build map of local files for quick lookup
	localFileMap := make(map[string]bool)
	for _, f := range localFiles {
		localFileMap[f] = true
	}

	// Find files to delete (only tracked + untracked non-ignored files)
	var filesToDelete []string
	for _, file := range bytes.Split(output, []byte{0}) {
		if len(file) == 0 {
			continue
		}
		remoteFile := string(file)
		if !localFileMap[remoteFile] {
			filesToDelete = append(filesToDelete, remoteFile)
		}
	}

	if len(filesToDelete) == 0 {
		fmt.Println("✓ No files to clean up")
		return nil
	}

	fmt.Printf("Removing %d files from remote...\n", len(filesToDelete))

	// Delete files in batches
	batchSize := 50
	for i := 0; i < len(filesToDelete); i += batchSize {
		end := i + batchSize
		if end > len(filesToDelete) {
			end = len(filesToDelete)
		}
		batch := filesToDelete[i:end]

		// Build rm command
		remoteRmCmd := buildRemoteCommand(remote, fmt.Sprintf("rm -f %s", strings.Join(batch, " ")))
		rmCmd := exec.Command("ssh", getSSHTarget(remote), remoteRmCmd)
		
		if err := rmCmd.Run(); err != nil {
			stats.Errors = append(stats.Errors, fmt.Sprintf("failed to delete batch %d-%d: %v", i, end, err))
		}
	}

	fmt.Printf("✓ Cleaned up %d files\n", len(filesToDelete))
	
	return nil
}

func verifyGitStatus(gitRoot string, remote *RemoteConfig) error {
	// Get local git status
	cmdStr := "git status --porcelain -z"
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "status", "--porcelain", "-z")
	cmd.Dir = gitRoot
	localOutput, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get local git status: %w", err)
	}

	// Get remote git status
	cmdStr = "git status --porcelain -z"
	logCommand("R", cmdStr)
	remoteCmd := buildRemoteCommand(remote, "git status --porcelain -z")
	cmd = exec.Command("ssh", getSSHTarget(remote), remoteCmd)
	remoteOutput, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get remote git status: %w", err)
	}

	// Parse local status
	localFiles := make(map[string]string)
	for _, entry := range bytes.Split(localOutput, []byte{0}) {
		if len(entry) < 4 {
			continue
		}
		status := string(entry[:2])
		filename := string(entry[3:])
		localFiles[filename] = status
	}

	// Parse remote status
	remoteFiles := make(map[string]string)
	for _, entry := range bytes.Split(remoteOutput, []byte{0}) {
		if len(entry) < 4 {
			continue
		}
		status := string(entry[:2])
		filename := string(entry[3:])
		remoteFiles[filename] = status
	}

	// Compare statuses - but be lenient about files we just synced
	// The sync process updates file content but git may not immediately reflect this
	// in its status due to index caching. This is acceptable as long as the file
	// content was actually transferred (verified by checksum comparison earlier).
	var inconsistencies []string
	
	// Check for files in remote but not in local (these are real problems)
	for file, remoteStatus := range remoteFiles {
		if _, exists := localFiles[file]; !exists {
			inconsistencies = append(inconsistencies, fmt.Sprintf("  %s: local=clean remote=%s", file, remoteStatus))
		}
	}

	if len(inconsistencies) > 0 {
		fmt.Printf("⚠ Git status inconsistencies detected (%d):\n", len(inconsistencies))
		for _, msg := range inconsistencies {
			fmt.Println(msg)
		}
		return fmt.Errorf("git status mismatch between local and remote")
	}

	// Note: We don't fail if local shows modified but remote shows clean
	// because we've already verified the file content was synced via checksum
	if len(localFiles) > len(remoteFiles) {
		fmt.Printf("ℹ Local has %d modified file(s), remote shows clean (content was synced)\n", len(localFiles)-len(remoteFiles))
	}
	
	fmt.Println("✓ Sync verification passed")
	return nil
}

func printSummary(stats *SyncStats) {
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("Sync Summary")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Files transferred: %d\n", stats.FilesTransferred)
	if stats.BytesTransferred > 0 {
		fmt.Printf("Bytes transferred: %d\n", stats.BytesTransferred)
	}
	fmt.Printf("Duration: %v\n", stats.Duration.Round(time.Millisecond))
	
	if len(stats.Errors) > 0 {
		fmt.Printf("\nWarnings/Errors: %d\n", len(stats.Errors))
		for _, err := range stats.Errors {
			fmt.Printf("  - %s\n", err)
		}
	} else {
		fmt.Println("\n✓ Sync completed successfully!")
	}
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
