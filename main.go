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
	version        = "1.3.0"
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
	// Define command-line flags
	remotePath := flag.String("remote-path", "", "Remote path in format user@host:/path (overrides config file)")
	remoteSetup := flag.String("remote-setup", "", "Remote setup command (overrides config file)")
	showHelp := flag.Bool("h", false, "Show help message")
	showVersion := flag.Bool("version", false, "Show version information")
	verboseFlag := flag.Bool("v", false, "Verbose mode - show all git commands")
	
	flag.Parse()

	verbose = *verboseFlag

	if *showHelp {
		printHelp()
		return
	}

	if *showVersion {
		fmt.Printf("git-remote-sync version %s\n", version)
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

	fmt.Printf("Remote: %s@%s:%s\n", remote.User, remote.Host, remote.Path)

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

func printHelp() {
	fmt.Printf(`git-remote-sync version %s

Usage: git-remote-sync [options]

Syncs a local git repository with a remote one via SSH, maintaining the exact
state including modified files, untracked files, and current branch.

Configuration can be provided via a file named '%s' in the repository root
or via command-line flags (flags override file settings).

Config file format:
  remote-path:userid@hostname:/path/to/remote/repo
  remote-setup:. ./.env

Options:
  -h, -help              Show this help message
  -version               Show version information
  -v                     Verbose mode - show all git commands (prefixed with L: or R:)
  -remote-path string    Remote path in format user@host:/path (overrides config file)
  -remote-setup string   Remote setup command (overrides config file)

The remote-setup is optional and allows you to run environment setup commands
before each remote operation (e.g., sourcing environment files).

Examples:
  # Use config file
  git-remote-sync

  # Override remote path
  git-remote-sync -remote-path user@host:/path/to/repo

  # Provide all config via command line
  git-remote-sync -remote-path user@host:/path -remote-setup ". ./.env"

The utility handles EBCDIC/ASCII conversion for z/OS systems automatically
using iconv (codepage 1047 to 819).
`, version, remoteSyncFile)
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

func readRemoteConfig(path string, cmdRemotePath string, cmdRemoteSetup string) (*RemoteConfig, error) {
	config := &RemoteConfig{}
	
	// Try to read from file first (if it exists)
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
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
				if cmdRemotePath == "" {
					// Parse user@host:/path
					pathParts := strings.SplitN(value, ":", 2)
					if len(pathParts) != 2 {
						return nil, fmt.Errorf("invalid remote-path format, expected: user@host:/path")
					}

					userHost := pathParts[0]
					remotePath := pathParts[1]

					userHostParts := strings.SplitN(userHost, "@", 2)
					if len(userHostParts) != 2 {
						return nil, fmt.Errorf("invalid remote-path format, expected: user@host:/path")
					}

					config.User = userHostParts[0]
					config.Host = userHostParts[1]
					config.Path = remotePath
					config.FullSpec = value
				}

			case "remote-setup":
				if cmdRemoteSetup == "" {
					config.Setup = value
				}
			}
		}
	}

	// Override with command-line arguments if provided
	if cmdRemotePath != "" {
		pathParts := strings.SplitN(cmdRemotePath, ":", 2)
		if len(pathParts) != 2 {
			return nil, fmt.Errorf("invalid --remote-path format, expected: user@host:/path")
		}

		userHost := pathParts[0]
		remotePath := pathParts[1]

		userHostParts := strings.SplitN(userHost, "@", 2)
		if len(userHostParts) != 2 {
			return nil, fmt.Errorf("invalid --remote-path format, expected: user@host:/path")
		}

		config.User = userHostParts[0]
		config.Host = userHostParts[1]
		config.Path = remotePath
		config.FullSpec = cmdRemotePath
	}

	if cmdRemoteSetup != "" {
		config.Setup = cmdRemoteSetup
	}

	// Validate that we have required configuration
	if config.User == "" || config.Host == "" || config.Path == "" {
		if err != nil {
			return nil, fmt.Errorf("cannot open %s and no --remote-path provided: %w", remoteSyncFile, err)
		}
		return nil, fmt.Errorf("missing required remote-path configuration (provide via file or --remote-path flag)")
	}

	return config, nil
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
	// Test SSH connection and check if remote path exists
	// Don't use buildRemoteCommand here since we need to verify the path exists first
	testCmd := fmt.Sprintf("test -d %s && echo OK || echo NOTFOUND", remote.Path)
	cmd := exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), testCmd)
	
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

	return nil
}

func buildRemoteCommand(remote *RemoteConfig, command string) string {
	if remote.Setup != "" {
		return fmt.Sprintf("%s && cd %s && %s", remote.Setup, remote.Path, command)
	}
	return fmt.Sprintf("cd %s && %s", remote.Path, command)
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
	cmd := exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
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
		filename := line[3:]
		
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

	// Remote is behind - need to sync commits
	// Git push won't work with remote-setup (git-receive-pack needs git in PATH)
	// Use bundle method which allows running setup commands before git operations

	// Create a minimal bundle with only the current branch
	bundlePath := filepath.Join(os.TempDir(), fmt.Sprintf("git-sync-%d.bundle", time.Now().Unix()))
	defer os.Remove(bundlePath)

	// Create bundle with only current branch (not --all) to minimize size
	cmdStr := fmt.Sprintf("git bundle create %s %s", bundlePath, branch)
	logCommand("L", cmdStr)
	cmd := exec.Command("git", "bundle", "create", bundlePath, branch)
	cmd.Dir = gitRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create bundle: %w\nOutput: %s", err, string(output))
	}

	// Transfer bundle to remote using cat over SSH to avoid scp text mode issues on z/OS
	// This ensures binary transfer without EBCDIC conversion
	transferCmd := fmt.Sprintf("cat %s | ssh %s@%s 'cat > /tmp/git-sync.bundle'", bundlePath, remote.User, remote.Host)
	cmd = exec.Command("bash", "-c", transferCmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to transfer bundle: %w\nOutput: %s", err, string(output))
	}

	// Apply bundle on remote: unbundle → update refs → reset to commit
	gitCmd := fmt.Sprintf("git bundle unbundle /tmp/git-sync.bundle && git update-ref refs/heads/%s %s && git symbolic-ref HEAD refs/heads/%s && git reset --hard %s", branch, localHead, branch, localHead)
	logCommand("R", gitCmd)
	remoteCmd := buildRemoteCommand(remote, fmt.Sprintf("git bundle unbundle /tmp/git-sync.bundle >/dev/null 2>&1 && rm /tmp/git-sync.bundle && git update-ref refs/heads/%s %s >/dev/null 2>&1 && git symbolic-ref HEAD refs/heads/%s >/dev/null 2>&1 && git reset --hard %s >/dev/null 2>&1 && echo 'OK' || echo 'FAILED'", branch, localHead, branch, localHead))
	cmd = exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
	output, err := cmd.CombinedOutput()
	
	outputStr := strings.TrimSpace(string(output))
	if err != nil || !strings.Contains(outputStr, "OK") {
		return fmt.Errorf("failed to apply bundle on remote: %v\nOutput: %s", err, outputStr)
	}

	fmt.Printf("✓ Synced commits to: %s\n", localHead[:8])
	return nil
}

func ensureRemoteBranch(remote *RemoteConfig, branch string) error {
	// Check if remote is a git repository
	gitCmd := "git rev-parse --git-dir"
	logCommand("R", gitCmd)
	remoteCmd := buildRemoteCommand(remote, "git rev-parse --git-dir > /dev/null 2>&1 && echo OK || echo NOTGIT")
	cmd := exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
	
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
	cmd = exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
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
	cmd = exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
	output, _ = cmd.Output()
	
	branchExists := strings.TrimSpace(string(output)) == "EXISTS"
	
	if branchExists {
		// Branch exists, checkout and restore all files to match HEAD, then stage all changes
		gitCmd = fmt.Sprintf("git checkout %s && git reset --hard HEAD && git clean -fd && git add -A", branch)
		logCommand("R", gitCmd)
		remoteCmd = buildRemoteCommand(remote, fmt.Sprintf("git checkout %s >/dev/null 2>/dev/null && git reset --hard HEAD >/dev/null 2>/dev/null && git clean -fd >/dev/null 2>/dev/null && git add -A >/dev/null 2>/dev/null && echo 'OK' || echo 'FAILED'", branch))
		cmd = exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
		output, _ = cmd.Output()
		
		result := strings.TrimSpace(string(output))
		if result == "OK" {
			fmt.Printf("✓ Remote branch: %s\n", branch)
		} else {
			fmt.Printf("⚠ Remote branch checkout failed: %s\n", branch)
		}
	} else {
		// Branch doesn't exist, create it and restore all files, then stage all changes
		gitCmd = fmt.Sprintf("git checkout -b %s && git reset --hard HEAD && git clean -fd && git add -A", branch)
		logCommand("R", gitCmd)
		remoteCmd = buildRemoteCommand(remote, fmt.Sprintf("git checkout -b %s >/dev/null 2>/dev/null && git reset --hard HEAD >/dev/null 2>/dev/null && git clean -fd >/dev/null 2>/dev/null && git add -A >/dev/null 2>/dev/null && echo 'CREATED' || echo 'FAILED'", branch))
		cmd = exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
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

func syncGitDirectory(gitRoot string, remote *RemoteConfig, stats *SyncStats) error {
	// Create tar archive of .git directory with EBCDIC conversion
	tarCmd := fmt.Sprintf("cd %s && tar -cf - .git 2>/dev/null", gitRoot)
	
	// Check if iconv is available (indicates z/OS target)
	remoteCheckCmd := buildRemoteCommand(remote, "which /bin/iconv > /dev/null 2>&1 && echo ZOS || echo UNIX")
	checkCmd := exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCheckCmd)
	output, _ := checkCmd.Output()
	isZOS := strings.TrimSpace(string(output)) == "ZOS"

	var cmd *exec.Cmd
	if isZOS {
		// z/OS: use iconv for EBCDIC to ASCII conversion (codepage 1047 to 819)
		remoteTarCmd := buildRemoteCommand(remote, "/bin/iconv -f 1047 -t 819 | /bin/tar -xvfUX - 2>&1")
		fullCmd := fmt.Sprintf("%s | ssh %s@%s '%s'",
			tarCmd, remote.User, remote.Host, remoteTarCmd)
		cmd = exec.Command("bash", "-c", fullCmd)
	} else {
		// Unix: direct tar transfer
		remoteTarCmd := buildRemoteCommand(remote, "tar -xf - 2>&1")
		fullCmd := fmt.Sprintf("%s | ssh %s@%s '%s'",
			tarCmd, remote.User, remote.Host, remoteTarCmd)
		cmd = exec.Command("bash", "-c", fullCmd)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar transfer failed: %w\nOutput: %s", err, string(output))
	}

	fmt.Println("✓ .git directory synced")
	return nil
}

func syncFiles(gitRoot string, remote *RemoteConfig, files []string, stats *SyncStats) error {
	// Check if remote is z/OS
	remoteCheckCmd := buildRemoteCommand(remote, "which /bin/iconv > /dev/null 2>&1 && echo ZOS || echo UNIX")
	checkCmd := exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCheckCmd)
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

	// Sync files in batches using tar
	batchSize := 100
	for i := 0; i < len(filesToSync); i += batchSize {
		end := i + batchSize
		if end > len(filesToSync) {
			end = len(filesToSync)
		}
		batch := filesToSync[i:end]

		if err := syncFileBatch(gitRoot, remote, batch, isZOS == "ZOS", stats); err != nil {
			stats.Errors = append(stats.Errors, fmt.Sprintf("batch %d-%d: %v", i, end, err))
			// Continue with next batch
		} else {
			stats.FilesTransferred += len(batch)
			fmt.Printf("✓ Synced files %d-%d of %d\n", i+1, end, len(filesToSync))
		}
	}

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
	cmd := exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
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

func syncFileBatch(gitRoot string, remote *RemoteConfig, files []string, isZOS bool, stats *SyncStats) error {
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

	var cmd *exec.Cmd
	if isZOS {
		// z/OS: use iconv for EBCDIC to ASCII conversion (codepage 1047 to 819)
		remoteTarCmd := buildRemoteCommand(remote, "/bin/iconv -f 1047 -t 819 | /bin/tar -xvfUX - 2>&1")
		fullCmd := fmt.Sprintf("%s | ssh %s@%s '%s'",
			tarCmd, remote.User, remote.Host, remoteTarCmd)
		cmd = exec.Command("bash", "-c", fullCmd)
	} else {
		// Unix: direct tar transfer
		remoteTarCmd := buildRemoteCommand(remote, "tar -xf - 2>&1")
		fullCmd := fmt.Sprintf("%s | ssh %s@%s '%s'",
			tarCmd, remote.User, remote.Host, remoteTarCmd)
		cmd = exec.Command("bash", "-c", fullCmd)
	}

	output, err := cmd.CombinedOutput()
	outputStr := string(output)
	
	// On z/OS, tar may report "cannot set uid/gid" even with 'o' flag, but file is extracted
	// Check if extraction was successful despite the error
	if err != nil && isZOS {
		if strings.Contains(outputStr, "cannot set uid/gid") && strings.Contains(outputStr, "x ") {
			// File was extracted (indicated by "x " in output), ownership error is non-fatal
			fmt.Printf("  (ignoring ownership error on z/OS)\n")
			return nil
		}
	}
	
	if err != nil {
		return fmt.Errorf("tar transfer failed: %w\nOutput: %s", err, outputStr)
	}

	return nil
}

func cleanupRemote(gitRoot string, remote *RemoteConfig, localFiles []string, stats *SyncStats) error {
	// Get list of files on remote
	remoteFindCmd := buildRemoteCommand(remote, fmt.Sprintf("find . -type f ! -path './.git/*' ! -name '%s' 2>/dev/null", remoteSyncFile))
	cmd := exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteFindCmd)
	
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list remote files: %w", err)
	}

	// Build map of local files for quick lookup
	localFileMap := make(map[string]bool)
	for _, f := range localFiles {
		localFileMap[f] = true
	}

	// Find files to delete
	var filesToDelete []string
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		remoteFile := strings.TrimPrefix(scanner.Text(), "./")
		if remoteFile == "" || remoteFile == "." {
			continue
		}
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
		cmd := exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteRmCmd)
		
		if err := cmd.Run(); err != nil {
			stats.Errors = append(stats.Errors, fmt.Sprintf("failed to delete batch %d-%d: %v", i, end, err))
		}
	}

	fmt.Printf("✓ Cleaned up %d files\n", len(filesToDelete))
	
	// After cleanup, reset git index to match HEAD to clear any staged deletions
	remoteResetCmd := buildRemoteCommand(remote, "git reset HEAD >/dev/null 2>/dev/null && git checkout -- . >/dev/null 2>/dev/null || true")
	cmd = exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteResetCmd)
	_ = cmd.Run() // Ignore errors, this is best-effort cleanup
	
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
	cmd = exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
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

	// Compare statuses
	var inconsistencies []string
	
	// Check for files in local but not in remote or with different status
	for file, localStatus := range localFiles {
		if remoteStatus, exists := remoteFiles[file]; !exists {
			inconsistencies = append(inconsistencies, fmt.Sprintf("  %s: local=%s remote=clean", file, localStatus))
		} else if localStatus != remoteStatus {
			inconsistencies = append(inconsistencies, fmt.Sprintf("  %s: local=%s remote=%s", file, localStatus, remoteStatus))
		}
	}

	// Check for files in remote but not in local
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

	fmt.Println("✓ Git status is consistent between local and remote")
	return nil
}

func printSummary(stats *SyncStats) {
	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("Sync Summary")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Files transferred: %d\n", stats.FilesTransferred)
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