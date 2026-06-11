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
	version        = "1.0.0"
)

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
	showVersion := flag.Bool("v", false, "Show version information")
	
	flag.Parse()

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
  -v, -version           Show version information
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

func getGitRoot() (string, error) {
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

	// Get list of modified and untracked files
	files, err := getFilesToSync(gitRoot)
	if err != nil {
		return fmt.Errorf("failed to get files to sync: %w", err)
	}

	if len(files) == 0 {
		fmt.Println("\nNo modified or untracked files to sync")
		return nil
	}

	fmt.Printf("\nFiles to sync: %d\n", len(files))

	// Sync working tree files
	fmt.Println("\nSyncing working tree files...")
	if err := syncFiles(gitRoot, remote, files, stats); err != nil {
		return fmt.Errorf("failed to sync files: %w", err)
	}

	return nil
}

func getLocalHead(gitRoot string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func getRemoteHead(remote *RemoteConfig) (string, error) {
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

	// Get modified, added, deleted, renamed files using git status
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 4 {
			continue
		}
		// Format: XY filename or XY old -> new
		// Extract filename(s)
		parts := strings.Fields(line[3:])
		if len(parts) > 0 {
			// Handle renames (old -> new)
			if strings.Contains(line, " -> ") {
				// Get both old and new filenames
				renameParts := strings.Split(line[3:], " -> ")
				if len(renameParts) == 2 {
					fileSet[strings.TrimSpace(renameParts[1])] = true
				}
			} else {
				fileSet[parts[0]] = true
			}
		}
	}

	// Get untracked files
	cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = gitRoot
	output, err = cmd.Output()
	if err != nil {
		return nil, err
	}

	scanner = bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fileSet[scanner.Text()] = true
	}

	// Convert set to slice
	for file := range fileSet {
		files = append(files, file)
	}

	return files, nil
}

func ensureRemoteBranch(remote *RemoteConfig, branch string) error {
	// Check if remote is a git repository
	remoteCmd := buildRemoteCommand(remote, "git rev-parse --git-dir > /dev/null 2>&1 && echo OK || echo NOTGIT")
	cmd := exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to verify remote git repo: %w", err)
	}

	if strings.Contains(string(output), "NOTGIT") {
		return fmt.Errorf("remote path is not a git repository")
	}

	// Try to checkout the branch (don't fetch, we'll sync .git if needed)
	// Suppress all output to avoid EBCDIC encoding issues on z/OS
	remoteCmd = buildRemoteCommand(remote, fmt.Sprintf("git checkout %s >/dev/null 2>/dev/null && echo 'OK' || echo 'FAILED'", branch))
	cmd = exec.Command("ssh", fmt.Sprintf("%s@%s", remote.User, remote.Host), remoteCmd)
	output, _ = cmd.Output()
	
	result := strings.TrimSpace(string(output))
	if result == "OK" {
		fmt.Printf("✓ Remote branch: %s\n", branch)
	} else if result == "FAILED" {
		fmt.Printf("⚠ Remote branch checkout failed: %s\n", branch)
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