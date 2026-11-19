package main

// Sync directions beyond the original local-to-remote mirror:
//   r2l   - make the local repository match the remote one
//   merge - exchange changes both ways; per file, the side that changed it
//           (deviates from HEAD) wins, and when both did the newer file wins

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Direction int

const (
	dirLocalToRemote Direction = iota
	dirRemoteToLocal
	dirMerge
)

func parseDirection(s string) (Direction, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "l2r", "local-to-remote", "push":
		return dirLocalToRemote, nil
	case "r2l", "remote-to-local", "pull":
		return dirRemoteToLocal, nil
	case "merge", "merge-remote-local":
		return dirMerge, nil
	}
	return 0, fmt.Errorf("unknown direction %q (use l2r, r2l or merge)", s)
}

func directionName(d Direction) string {
	switch d {
	case dirRemoteToLocal:
		return "remote to local (r2l)"
	case dirMerge:
		return "merge remote and local"
	}
	return "local to remote (l2r)"
}

// ---------------------------------------------------------------------------
// Repository state helpers
// ---------------------------------------------------------------------------

// dropInternal removes paths that are never synced from a status map.
func dropInternal(m map[string]string) map[string]string {
	delete(m, remoteSyncFile)
	for k := range m {
		if strings.HasPrefix(k, ".git/") {
			delete(m, k)
		}
	}
	return m
}

// localDirty returns the paths that deviate from local HEAD (modified, staged,
// deleted, untracked; ignored files excluded) with their porcelain status.
func localDirty(gitRoot string) (map[string]string, error) {
	out, err := localGit(gitRoot, "status", "--porcelain", "-z", "--no-renames", "-uall")
	if err != nil {
		return nil, err
	}
	return dropInternal(parsePorcelainZ([]byte(out))), nil
}

// remoteDirty is localDirty for the remote repository.
func remoteDirty(remote *RemoteConfig) (map[string]string, error) {
	out, err := remoteRun(remote, "git status --porcelain -z --no-renames -uall")
	if err != nil {
		return nil, err
	}
	return dropInternal(parsePorcelainZ([]byte(out))), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// localHashes returns the git blob id of each file ("-" when it is missing
// or not a regular file).
func localHashes(gitRoot string, files []string) (map[string]string, error) {
	res := make(map[string]string, len(files))
	var present []string
	for _, f := range files {
		st, err := os.Stat(filepath.Join(gitRoot, f))
		if err != nil || !st.Mode().IsRegular() || strings.ContainsAny(f, "\n") {
			res[f] = "-"
			continue
		}
		present = append(present, f)
	}
	if len(present) == 0 {
		return res, nil
	}
	logCommand("L", "git hash-object --no-filters --stdin-paths")
	cmd := exec.Command("git", "hash-object", "--no-filters", "--stdin-paths")
	cmd.Dir = gitRoot
	cmd.Stdin = strings.NewReader(strings.Join(present, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git hash-object failed: %w", err)
	}
	hashes := strings.Fields(string(out))
	if len(hashes) != len(present) {
		return nil, fmt.Errorf("git hash-object returned %d ids for %d files", len(hashes), len(present))
	}
	for i, f := range present {
		res[f] = hashes[i]
	}
	return res, nil
}

// remoteHashes is localHashes for the remote repository.
func remoteHashes(remote *RemoteConfig, files []string) (map[string]string, error) {
	res := make(map[string]string, len(files))
	const batchSize = 50
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
		script := fmt.Sprintf(`for f in %s; do if [ -f "$f" ]; then git hash-object --no-filters -- "$f" 2>/dev/null || echo -; else echo -; fi; done`,
			strings.Join(quoted, " "))
		out, err := remoteRun(remote, script)
		if err != nil {
			return nil, fmt.Errorf("remote hash failed: %w", err)
		}
		lines := strings.Fields(out)
		if len(lines) != len(batch) {
			return nil, fmt.Errorf("remote returned %d hashes for %d files", len(lines), len(batch))
		}
		for j, f := range batch {
			res[f] = lines[j]
		}
	}
	return res, nil
}

// treeBlobs maps every file of commit to its blob id.
func treeBlobs(gitRoot, commit string) (map[string]string, error) {
	out, err := localGit(gitRoot, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return nil, err
	}
	res := make(map[string]string)
	for _, entry := range strings.Split(out, "\x00") {
		tab := strings.IndexByte(entry, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(entry[:tab])
		if len(fields) == 3 {
			res[entry[tab+1:]] = fields[2]
		}
	}
	return res, nil
}

// diffPaths returns path -> status letter for the files that differ between
// two commits.
func diffPaths(gitRoot, a, b string) (map[string]string, error) {
	res := make(map[string]string)
	if a == b {
		return res, nil
	}
	out, err := localGit(gitRoot, "diff", "--name-status", "-z", "--no-renames", a, b)
	if err != nil {
		return nil, err
	}
	toks := strings.Split(out, "\x00")
	for i := 0; i+1 < len(toks); i += 2 {
		res[toks[i+1]] = toks[i]
	}
	return res, nil
}

func isAncestor(gitRoot, a, b string) bool {
	_, err := localGit(gitRoot, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

func getRemoteBranch(remote *RemoteConfig) string {
	out, err := remoteRun(remote, "git rev-parse --abbrev-ref HEAD 2>/dev/null")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// removeFromRemote deletes files on the remote in batches.
func removeFromRemote(remote *RemoteConfig, files []string) error {
	const batchSize = 50
	for i := 0; i < len(files); i += batchSize {
		end := i + batchSize
		if end > len(files) {
			end = len(files)
		}
		quoted := make([]string, 0, end-i)
		for _, f := range files[i:end] {
			quoted = append(quoted, shellQuote(f))
		}
		out, err := remoteRun(remote, fmt.Sprintf("%s -f %s && echo OK", rt(remote, "rm"), strings.Join(quoted, " ")))
		if err != nil || !strings.Contains(out, "OK") {
			return fmt.Errorf("remote rm failed: %v", err)
		}
	}
	return nil
}

// removeLocal deletes files below gitRoot (missing files are fine).
func removeLocal(gitRoot string, files []string) {
	for _, f := range files {
		os.Remove(filepath.Join(gitRoot, f))
	}
}

// checkoutPaths restores paths from commit in a repository (index + working
// tree). run executes the git command line given as arguments.
func checkoutBatches(files []string, size int, run func(batch []string) error) error {
	for i := 0; i < len(files); i += size {
		end := i + size
		if end > len(files) {
			end = len(files)
		}
		if err := run(files[i:end]); err != nil {
			return err
		}
	}
	return nil
}

// moveLocalHead points the local branch at target. With hard it also resets
// index and working tree; otherwise the working tree is left alone, and the
// given paths are restored from target (restore) or deleted (remove).
func moveLocalHead(gitRoot, branch, target string, hard bool, restore, remove []string) error {
	if out, err := localGitCombined(gitRoot, "update-ref", "refs/heads/"+branch, target); err != nil {
		return fmt.Errorf("update-ref failed: %v\n%s", err, out)
	}
	if out, err := localGitCombined(gitRoot, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("symbolic-ref failed: %v\n%s", err, out)
	}
	resetArg := "--mixed"
	if hard {
		resetArg = "--hard"
	}
	if out, err := localGitCombined(gitRoot, "reset", "-q", resetArg, target); err != nil {
		return fmt.Errorf("reset failed: %v\n%s", err, out)
	}
	removeLocal(gitRoot, remove)
	return checkoutBatches(restore, 200, func(batch []string) error {
		args := append([]string{"--literal-pathspecs", "checkout", target, "--"}, batch...)
		if out, err := localGitCombined(gitRoot, args...); err != nil {
			return fmt.Errorf("checkout failed: %v\n%s", err, out)
		}
		return nil
	})
}

// moveRemoteHead is moveLocalHead for the remote repository (mixed reset).
func moveRemoteHead(remote *RemoteConfig, branch, target string, restore, remove []string) error {
	script := fmt.Sprintf("git update-ref refs/heads/%s %s >/dev/null 2>&1 && git symbolic-ref HEAD refs/heads/%s >/dev/null 2>&1 && git reset -q --mixed %s >/dev/null 2>&1 && echo OK || echo FAILED",
		branch, target, branch, target)
	out, err := remoteRun(remote, script)
	if err != nil || !strings.Contains(out, "OK") {
		return fmt.Errorf("failed to move remote HEAD: %v %s", err, strings.TrimSpace(out))
	}
	if err := removeFromRemote(remote, remove); err != nil {
		return err
	}
	return checkoutBatches(restore, 50, func(batch []string) error {
		quoted := make([]string, len(batch))
		for i, f := range batch {
			quoted[i] = shellQuote(f)
		}
		s := fmt.Sprintf("git --literal-pathspecs checkout %s -- %s >/dev/null 2>&1 && echo OK || echo FAILED", target, strings.Join(quoted, " "))
		out, err := remoteRun(remote, s)
		if err != nil || !strings.Contains(out, "OK") {
			return fmt.Errorf("remote checkout failed: %v %s", err, strings.TrimSpace(out))
		}
		return nil
	})
}

// compareMtimes compares the modification time of files on the remote with
// the local ones (second resolution). Result per file: "L" local is newer,
// "R" remote is newer, "=" same second.
func compareMtimes(gitRoot string, remote *RemoteConfig, files []string) (map[string]string, error) {
	res := make(map[string]string, len(files))
	const batchSize = 40
	for i := 0; i < len(files); i += batchSize {
		end := i + batchSize
		if end > len(files) {
			end = len(files)
		}
		batch := files[i:end]
		ref := ".git/remote-sync.ref"
		var sb strings.Builder
		sb.WriteString("( ")
		for j, f := range batch {
			st, err := os.Stat(filepath.Join(gitRoot, f))
			if err != nil {
				return nil, err
			}
			stamp := st.ModTime().UTC().Format("200601021504.05")
			q := shellQuote(f)
			fmt.Fprintf(&sb, `TZ=UTC0 %[1]s -t %[2]s %[3]s; if [ -n "$(%[4]s %[5]s -newer %[3]s)" ]; then echo R%[6]d; elif [ -n "$(%[4]s %[3]s -newer %[5]s)" ]; then echo L%[6]d; else echo E%[6]d; fi; `,
				rt(remote, "touch"), stamp, ref, rt(remote, "find"), q, j)
		}
		fmt.Fprintf(&sb, "%s -f %s )", rt(remote, "rm"), ref)
		out, err := remoteRun(remote, sb.String())
		if err != nil {
			return nil, fmt.Errorf("remote timestamp comparison failed: %w", err)
		}
		seen := 0
		for _, tok := range strings.Fields(out) {
			if len(tok) < 2 {
				continue
			}
			var idx int
			if _, err := fmt.Sscanf(tok[1:], "%d", &idx); err != nil || idx < 0 || idx >= len(batch) {
				continue
			}
			switch tok[0] {
			case 'R':
				res[batch[idx]] = "R"
			case 'L':
				res[batch[idx]] = "L"
			case 'E':
				res[batch[idx]] = "="
			default:
				continue
			}
			seen++
		}
		if seen != len(batch) {
			return nil, fmt.Errorf("remote timestamp comparison returned %d of %d results", seen, len(batch))
		}
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// remote-to-local
// ---------------------------------------------------------------------------

// syncRemoteToLocal makes the local repository (HEAD, working tree, untracked
// files) match the remote one. Local-only changes are discarded.
func syncRemoteToLocal(gitRoot string, remote *RemoteConfig, localBranch string, stats *SyncStats) error {
	rHead, err := getRemoteHead(remote)
	if err != nil {
		return fmt.Errorf("cannot read remote HEAD: %w", err)
	}
	branch := getRemoteBranch(remote)
	if branch == "" || branch == "HEAD" {
		branch = localBranch
	}
	if branch == "" || branch == "HEAD" {
		return fmt.Errorf("remote and local are both on a detached HEAD; check out a branch first")
	}

	fmt.Println("\nSyncing commit history from remote...")
	if err := shipCommitsToLocal(gitRoot, remote, rHead); err != nil {
		return fmt.Errorf("failed to get commits from remote: %w", err)
	}
	if branch != localBranch {
		fmt.Printf("Switching local branch to the remote's branch: %s\n", branch)
	}
	if err := guardLocalMove(gitRoot, branch, rHead); err != nil {
		return err
	}
	fmt.Println("Resetting local working tree to remote HEAD (local-only changes are discarded)...")
	if err := moveLocalHead(gitRoot, branch, rHead, true, nil, nil); err != nil {
		return err
	}
	fmt.Printf("✓ Local at commit: %s\n", rHead[:8])

	// Files that deviate from HEAD on the remote
	rDirty, err := remoteDirty(remote)
	if err != nil {
		return fmt.Errorf("failed to get remote git status: %w", err)
	}
	var wanted, deleted []string
	for _, f := range sortedKeys(rDirty) {
		if strings.Contains(rDirty[f], "D") {
			deleted = append(deleted, f)
		} else {
			wanted = append(wanted, f)
		}
	}
	removeLocal(gitRoot, deleted)

	if len(wanted) == 0 {
		fmt.Println("✓ No modified or untracked files on remote")
	} else {
		fmt.Printf("\nFiles on remote deviating from HEAD: %d\n", len(wanted))
		toGet, err := filesDiffering(gitRoot, remote, wanted)
		if err != nil {
			return err
		}
		if len(toGet) == 0 {
			fmt.Println("✓ All files are already in sync")
		} else {
			fmt.Printf("Downloading %d file(s) (skipped %d unchanged)...\n", len(toGet), len(wanted)-len(toGet))
			err := downloadFiles(remote, gitRoot, toGet, func(string) { stats.FilesTransferred++ })
			if err != nil {
				return err
			}
			fmt.Printf("✓ Downloaded %d file(s)\n", len(toGet))
		}
	}

	// Remove local files that do not exist on the remote
	fmt.Println("\nCleaning up local files...")
	out, err := remoteRun(remote, "git ls-files -z && git ls-files --others --exclude-standard -z")
	if err != nil {
		return fmt.Errorf("failed to list remote files: %w", err)
	}
	remoteAll := make(map[string]bool)
	for _, f := range bytes.Split([]byte(out), []byte{0}) {
		if len(f) > 0 {
			remoteAll[string(f)] = true
		}
	}
	for _, f := range deleted {
		delete(remoteAll, f)
	}
	localAll, err := getAllLocalFiles(gitRoot)
	if err != nil {
		return err
	}
	var stale []string
	for _, f := range localAll {
		if !remoteAll[f] && f != remoteSyncFile {
			stale = append(stale, f)
		}
	}
	removeLocal(gitRoot, stale)
	if len(stale)+len(deleted) == 0 {
		fmt.Println("✓ No files to clean up")
	} else {
		fmt.Printf("✓ Removed %d file(s)\n", len(stale)+len(deleted))
	}

	fmt.Println("\nVerifying sync consistency...")
	return verifyConverged(gitRoot, remote)
}

// filesDiffering returns those files whose content differs between remote and
// local (or is missing locally).
func filesDiffering(gitRoot string, remote *RemoteConfig, files []string) ([]string, error) {
	rh, err := remoteHashes(remote, files)
	if err != nil {
		return nil, err
	}
	lh, err := localHashes(gitRoot, files)
	if err != nil {
		return nil, err
	}
	var diff []string
	for _, f := range files {
		if rh[f] != lh[f] {
			diff = append(diff, f)
		}
	}
	return diff, nil
}

// verifyConverged checks that local and remote deviate from HEAD in the same
// files with the same content.
func verifyConverged(gitRoot string, remote *RemoteConfig) error {
	ld, err := localDirty(gitRoot)
	if err != nil {
		return err
	}
	rd, err := remoteDirty(remote)
	if err != nil {
		return err
	}
	var problems []string
	all := make(map[string]bool)
	for f := range ld {
		all[f] = true
	}
	for f := range rd {
		all[f] = true
	}
	var both []string
	for _, f := range sortedKeys(all) {
		_, inL := ld[f]
		_, inR := rd[f]
		switch {
		case inL && !inR:
			problems = append(problems, fmt.Sprintf("  %s: local=%s remote=clean", f, ld[f]))
		case !inL && inR:
			problems = append(problems, fmt.Sprintf("  %s: local=clean remote=%s", f, rd[f]))
		default:
			both = append(both, f)
		}
	}
	if len(both) > 0 {
		diff, err := filesDiffering(gitRoot, remote, both)
		if err != nil {
			return err
		}
		for _, f := range diff {
			problems = append(problems, fmt.Sprintf("  %s: content differs", f))
		}
	}
	if len(problems) > 0 {
		fmt.Printf("⚠ Inconsistencies detected (%d):\n%s\n", len(problems), strings.Join(problems, "\n"))
		return fmt.Errorf("local and remote differ after sync")
	}
	fmt.Println("✓ Sync verification passed")
	return nil
}

// ---------------------------------------------------------------------------
// merge
// ---------------------------------------------------------------------------

// MergeInput describes both sides for decideMerge. Hash values are git blob
// ids; "-" (or a missing key) means the file does not exist.
type MergeInput struct {
	DirtyL, DirtyR map[string]bool   // paths deviating from the side's own HEAD
	HashL, HashR   map[string]string // working tree hash of the dirty paths
	Star           map[string]string // blob ids in the merged commit
	ChgL, ChgR     map[string]bool   // paths the side's HEAD move changes
}

type MergeResult struct {
	PushRemote   []string // local content -> remote
	DeleteRemote []string
	PullLocal    []string // remote content -> local
	DeleteLocal  []string
	Warnings     []string
}

// decideMerge applies the merge rules per path:
//   - a side that did not deviate from HEAD takes what the other side has;
//   - if only one side deviates, that side wins;
//   - if both deviate and differ, an existing file beats a deleted one and
//     otherwise the newer modification time wins (a tie goes to local).
//
// mtime returns "L", "R" or "=" for each of the given paths.
func decideMerge(in MergeInput, mtime func(files []string) (map[string]string, error)) (*MergeResult, error) {
	star := func(p string) string {
		if h, ok := in.Star[p]; ok {
			return h
		}
		return "-"
	}
	final := func(dirty map[string]bool, hash map[string]string, p string) string {
		if dirty[p] {
			if h, ok := hash[p]; ok {
				return h
			}
			return "-"
		}
		return star(p)
	}
	union := make(map[string]bool)
	for p := range in.DirtyL {
		union[p] = true
	}
	for p := range in.DirtyR {
		union[p] = true
	}

	winner := make(map[string]string)
	var conflicts []string
	for _, p := range sortedKeys(union) {
		fL, fR := final(in.DirtyL, in.HashL, p), final(in.DirtyR, in.HashR, p)
		if fL == fR {
			continue
		}
		switch {
		case in.DirtyL[p] && !in.DirtyR[p]:
			winner[p] = "L"
		case in.DirtyR[p] && !in.DirtyL[p]:
			winner[p] = "R"
		case fL == "-":
			winner[p] = "R"
		case fR == "-":
			winner[p] = "L"
		default:
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) > 0 {
		cmp, err := mtime(conflicts)
		if err != nil {
			return nil, err
		}
		for _, p := range conflicts {
			if cmp[p] == "R" {
				winner[p] = "R"
			} else {
				winner[p] = "L"
			}
		}
	}

	res := &MergeResult{}
	for _, p := range sortedKeys(winner) {
		if winner[p] == "L" {
			if final(in.DirtyL, in.HashL, p) == "-" {
				res.DeleteRemote = append(res.DeleteRemote, p)
			} else {
				res.PushRemote = append(res.PushRemote, p)
			}
			if !in.DirtyR[p] && in.ChgL[p] {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: uncommitted local change overrides the incoming commit", p))
			}
		} else {
			if final(in.DirtyR, in.HashR, p) == "-" {
				res.DeleteLocal = append(res.DeleteLocal, p)
			} else {
				res.PullLocal = append(res.PullLocal, p)
			}
			if !in.DirtyL[p] && in.ChgR[p] {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: uncommitted remote change overrides the incoming commit", p))
			}
		}
	}
	return res, nil
}

func setOf(m map[string]string) map[string]bool {
	s := make(map[string]bool, len(m))
	for k := range m {
		s[k] = true
	}
	return s
}

// restoreLists splits the paths a HEAD move changes into those to check out
// from the target commit and those to delete, leaving out the side's own
// deviating files (their content is decided separately).
func restoreLists(chg map[string]string, dirty map[string]string) (restore, remove []string) {
	for _, p := range sortedKeys(chg) {
		if _, d := dirty[p]; d {
			continue
		}
		if chg[p] == "D" {
			remove = append(remove, p)
		} else {
			restore = append(restore, p)
		}
	}
	return
}

// syncMerge exchanges commits and working tree changes in both directions.
func syncMerge(gitRoot string, remote *RemoteConfig, branch string, stats *SyncStats) error {
	if branch == "" || branch == "HEAD" {
		return fmt.Errorf("merge needs a checked out branch (detached HEAD)")
	}
	localHead, err := getLocalHead(gitRoot)
	if err != nil {
		return fmt.Errorf("failed to get local HEAD: %w", err)
	}
	remoteHead, err := getRemoteHead(remote)
	if err != nil {
		return fmt.Errorf("remote has no commits (use -direction l2r first): %w", err)
	}
	if rb := getRemoteBranch(remote); rb != "" && rb != branch {
		fmt.Printf("ℹ Remote is on branch %s, local on %s: the remote will follow %s\n", rb, branch, branch)
	}

	// 1. Both repositories get all commits of both
	fmt.Println("\nExchanging commits...")
	if err := shipCommitsToRemote(gitRoot, remote, localHead); err != nil {
		return fmt.Errorf("failed to ship commits to remote: %w", err)
	}
	if err := shipCommitsToLocal(gitRoot, remote, remoteHead); err != nil {
		return fmt.Errorf("failed to get commits from remote: %w", err)
	}

	// 2. The commit both sides end up on
	hstar := localHead
	switch {
	case localHead == remoteHead:
		fmt.Printf("✓ Both at commit: %s\n", localHead[:8])
	case isAncestor(gitRoot, localHead, remoteHead):
		hstar = remoteHead
		fmt.Printf("✓ Local is behind: fast-forward to %s\n", hstar[:8])
	case isAncestor(gitRoot, remoteHead, localHead):
		fmt.Printf("✓ Remote is behind: fast-forward to %s\n", hstar[:8])
	default:
		out, err := localGitCombined(gitRoot, "merge-tree", "--write-tree", "--no-messages", localHead, remoteHead)
		if err != nil {
			return fmt.Errorf("local and remote history diverged and conflict; merge them by hand:\n%s", out)
		}
		tree := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
		msg := fmt.Sprintf("Merge remote %s into %s (git-remote-sync)", getSSHTarget(remote), branch)
		out, err = localGitCombined(gitRoot, "commit-tree", tree, "-p", localHead, "-p", remoteHead, "-m", msg)
		if err != nil {
			return fmt.Errorf("git commit-tree failed: %v\n%s", err, out)
		}
		hstar = strings.TrimSpace(out)
		if err := shipCommitsToRemote(gitRoot, remote, hstar); err != nil {
			return fmt.Errorf("failed to ship merge commit: %w", err)
		}
		fmt.Printf("✓ Histories diverged: merge commit %s\n", hstar[:8])
	}

	// 3. State of both working trees before anything moves
	lDirty, err := localDirty(gitRoot)
	if err != nil {
		return fmt.Errorf("failed to get local git status: %w", err)
	}
	rDirty, err := remoteDirty(remote)
	if err != nil {
		return fmt.Errorf("failed to get remote git status: %w", err)
	}
	chgL, err := diffPaths(gitRoot, localHead, hstar)
	if err != nil {
		return err
	}
	chgR, err := diffPaths(gitRoot, remoteHead, hstar)
	if err != nil {
		return err
	}
	star, err := treeBlobs(gitRoot, hstar)
	if err != nil {
		return err
	}
	lHash, err := localHashes(gitRoot, sortedKeys(lDirty))
	if err != nil {
		return err
	}
	rHash, err := remoteHashes(remote, sortedKeys(rDirty))
	if err != nil {
		return err
	}

	// 4. Per file decisions
	res, err := decideMerge(MergeInput{
		DirtyL: setOf(lDirty), DirtyR: setOf(rDirty),
		HashL: lHash, HashR: rHash, Star: star,
		ChgL: setOf(chgL), ChgR: setOf(chgR),
	}, func(files []string) (map[string]string, error) {
		return compareMtimes(gitRoot, remote, files)
	})
	if err != nil {
		return err
	}
	fmt.Printf("\nFiles: %d to remote, %d to local, %d deleted on remote, %d deleted locally\n",
		len(res.PushRemote), len(res.PullLocal), len(res.DeleteRemote), len(res.DeleteLocal))
	for _, w := range res.Warnings {
		fmt.Printf("⚠ %s\n", w)
		stats.Errors = append(stats.Errors, w)
	}

	// 5. Move HEADs, keeping the deviating files of each side
	if localHead != hstar {
		restore, remove := restoreLists(chgL, lDirty)
		if err := moveLocalHead(gitRoot, branch, hstar, false, restore, remove); err != nil {
			return err
		}
		fmt.Printf("✓ Local moved to %s\n", hstar[:8])
	}
	if remoteHead != hstar {
		restore, remove := restoreLists(chgR, rDirty)
		if err := moveRemoteHead(remote, branch, hstar, restore, remove); err != nil {
			return err
		}
		fmt.Printf("✓ Remote moved to %s\n", hstar[:8])
	}

	// 6. Transfer the winning files
	if len(res.PushRemote) > 0 {
		fmt.Println("\nSending files to remote...")
		if err := syncFiles(gitRoot, remote, res.PushRemote, stats); err != nil {
			return err
		}
	}
	if err := removeFromRemote(remote, res.DeleteRemote); err != nil {
		return err
	}
	stats.FilesTransferred += len(res.DeleteRemote)
	if len(res.PullLocal) > 0 {
		fmt.Println("\nReceiving files from remote...")
		if err := downloadFiles(remote, gitRoot, res.PullLocal, func(string) { stats.FilesTransferred++ }); err != nil {
			return err
		}
	}
	removeLocal(gitRoot, res.DeleteLocal)
	stats.FilesTransferred += len(res.DeleteLocal)

	// Symlinks: git on z/OS does not always restore them on checkout
	if err := syncSymlinks(gitRoot, remote, stats); err != nil {
		return fmt.Errorf("failed to sync symlinks: %w", err)
	}

	fmt.Println("\nVerifying sync consistency...")
	return verifyConverged(gitRoot, remote)
}
