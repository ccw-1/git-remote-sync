package main

// Commit transfer between local and remote without any network access from
// the remote: missing commits travel as a git bundle over the SSH connection.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	remoteBundleRel = ".git/remote-sync.bundle"
	syncTipRef      = "refs/remote-sync/tip"
	maxTips         = 100 // newest ref tips offered as bundle prerequisites
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// parseShas returns the valid, de-duplicated object names found in the lines
// of s, in order.
func parseShas(s string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if shaRe.MatchString(line) && !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	return out
}

// localGit runs git in the repository and returns stdout.
func localGit(gitRoot string, args ...string) (string, error) {
	logCommand("L", "git "+strings.Join(args, " "))
	cmd := exec.Command("git", args...)
	cmd.Dir = gitRoot
	out, err := cmd.Output()
	return string(out), err
}

// localGitCombined runs git and returns stdout+stderr (for error reporting).
func localGitCombined(gitRoot string, args ...string) (string, error) {
	logCommand("L", "git "+strings.Join(args, " "))
	cmd := exec.Command("git", args...)
	cmd.Dir = gitRoot
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// remoteRun runs a script on the remote in the repository directory and
// returns stdout. Multi-statement scripts must be wrapped in ( ... ).
func remoteRun(remote *RemoteConfig, script string) (string, error) {
	logCommand("R", script)
	cmd := sshCommand(getSSHTarget(remote), buildRemoteCommand(remote, script))
	out, err := cmd.Output()
	return string(out), err
}

// commitsAmongLines keeps the commits named in "sha type size" lines of
// git cat-file --batch-check output.
func commitsAmongLines(out string) []string {
	var res []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "commit" && shaRe.MatchString(f[0]) {
			res = append(res, f[0])
		}
	}
	return res
}

// localCommitsAmong returns those of shas that are commits in the local repo.
func localCommitsAmong(gitRoot string, shas []string) []string {
	if len(shas) == 0 {
		return nil
	}
	logCommand("L", "git cat-file --batch-check")
	cmd := exec.Command("git", "cat-file", "--batch-check")
	cmd.Dir = gitRoot
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	out, _ := cmd.Output()
	return commitsAmongLines(string(out))
}

// remoteCommitsAmong returns those of shas that are commits in the remote
// repo. The names are generated into the pipe by the remote shell itself, so
// no byte translation is involved.
func remoteCommitsAmong(remote *RemoteConfig, shas []string) []string {
	if len(shas) == 0 {
		return nil
	}
	out, _ := remoteRun(remote, "printf '%s\\n' "+strings.Join(shas, " ")+" | git cat-file --batch-check 2>/dev/null")
	return commitsAmongLines(out)
}

func localHasCommit(gitRoot, sha string) bool {
	return len(localCommitsAmong(gitRoot, []string{sha})) == 1
}

func remoteHasCommit(remote *RemoteConfig, sha string) bool {
	return len(remoteCommitsAmong(remote, []string{sha})) == 1
}

// localTips returns the newest ref tips (HEAD included) of the local repo.
func localTips(gitRoot string) []string {
	out, _ := localGit(gitRoot, "rev-list", "--no-walk=sorted", "--all")
	tips := parseShas(out)
	if len(tips) > maxTips {
		tips = tips[:maxTips]
	}
	return tips
}

// remoteTips returns the newest ref tips (HEAD included) of the remote repo.
func remoteTips(remote *RemoteConfig) []string {
	out, _ := remoteRun(remote, "git rev-list --no-walk=sorted --all 2>/dev/null")
	tips := parseShas(out)
	if len(tips) > maxTips {
		tips = tips[:maxTips]
	}
	return tips
}

func prerequisiteArgs(have []string) []string {
	args := make([]string, 0, len(have))
	for _, h := range have {
		args = append(args, "^"+h)
	}
	return args
}

// shipCommitsToRemote makes sure the remote repository contains commit tip
// (and its history) by sending a bundle of what the remote lacks.
func shipCommitsToRemote(gitRoot string, remote *RemoteConfig, tip string) error {
	if remoteHasCommit(remote, tip) {
		return nil
	}
	have := localCommitsAmong(gitRoot, remoteTips(remote))

	tmpDir, err := os.MkdirTemp("", "git-remote-sync-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	bundle := filepath.Join(tmpDir, "to-remote.bundle")

	// A bundle needs a ref name: use a temporary one for the tip.
	if _, err := localGitCombined(gitRoot, "update-ref", syncTipRef, tip); err != nil {
		return fmt.Errorf("cannot create temporary ref: %w", err)
	}
	args := append([]string{"bundle", "create", bundle, syncTipRef}, prerequisiteArgs(have)...)
	out, err := localGitCombined(gitRoot, args...)
	localGitCombined(gitRoot, "update-ref", "-d", syncTipRef)
	if err != nil {
		return fmt.Errorf("git bundle create failed: %v\n%s", err, out)
	}
	if st, err := os.Stat(bundle); err == nil {
		fmt.Printf("Sending %d bytes of commits to remote (%d common tip(s))...\n", st.Size(), len(have))
	}

	if err := uploadBinaryFile(remote, bundle, remoteBundleRel); err != nil {
		return err
	}
	script := fmt.Sprintf("( if git bundle unbundle %s >/dev/null 2>&1; then echo UNBUNDLED; else echo FAILED; fi; %s -f %s )",
		remoteBundleRel, rt(remote, "rm"), remoteBundleRel)
	res, err := remoteRun(remote, script)
	if err != nil || !strings.Contains(res, "UNBUNDLED") {
		return fmt.Errorf("remote git bundle unbundle failed: %v\nOutput: %s", err, strings.TrimSpace(res))
	}
	if !remoteHasCommit(remote, tip) {
		return fmt.Errorf("remote still lacks commit %s after unbundle", tip[:8])
	}
	fmt.Printf("✓ Remote received commits up to %s\n", tip[:8])
	return nil
}

// shipCommitsToLocal makes sure the local repository contains commit tip
// (and its history) by having the remote create a bundle of what we lack.
func shipCommitsToLocal(gitRoot string, remote *RemoteConfig, tip string) error {
	if localHasCommit(gitRoot, tip) {
		return nil
	}
	have := remoteCommitsAmong(remote, localTips(gitRoot))

	chtag := ""
	if remote.IsZOS {
		// the bundle is binary: make sure pax does not tag/convert it as text
		chtag = fmt.Sprintf("%s -b %s >/dev/null 2>&1; ", rt(remote, "chtag"), remoteBundleRel)
	}
	script := fmt.Sprintf("( if git update-ref %s %s && git bundle create %s %s %s >/dev/null 2>&1; then %secho CREATED; else echo FAILED; fi; git update-ref -d %s >/dev/null 2>&1 )",
		syncTipRef, tip, remoteBundleRel, syncTipRef, strings.Join(prerequisiteArgs(have), " "), chtag, syncTipRef)
	res, err := remoteRun(remote, script)
	if err != nil || !strings.Contains(res, "CREATED") {
		return fmt.Errorf("remote git bundle create failed: %v\nOutput: %s", err, strings.TrimSpace(res))
	}
	defer remoteRun(remote, fmt.Sprintf("%s -f %s", rt(remote, "rm"), remoteBundleRel))

	tmpDir, err := os.MkdirTemp("", "git-remote-sync-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	if err := downloadFiles(remote, tmpDir, []string{remoteBundleRel}, nil); err != nil {
		return err
	}
	bundle := filepath.Join(tmpDir, filepath.FromSlash(remoteBundleRel))
	if st, err := os.Stat(bundle); err == nil {
		fmt.Printf("Received %d bytes of commits from remote (%d common tip(s))\n", st.Size(), len(have))
	}
	if out, err := localGitCombined(gitRoot, "bundle", "unbundle", bundle); err != nil {
		return fmt.Errorf("git bundle unbundle failed: %v\n%s", err, out)
	}
	if !localHasCommit(gitRoot, tip) {
		return fmt.Errorf("local repository still lacks commit %s after unbundle", tip[:8])
	}
	fmt.Printf("✓ Local received commits up to %s\n", tip[:8])
	return nil
}
