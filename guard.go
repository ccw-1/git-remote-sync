package main

import (
	"fmt"
	"strings"
)

// forceMove (-force) allows l2r and r2l to move the target branch to a commit
// that is not a descendant of its current tip. The old tip is kept under
// refs/remote-sync/previous/<sha> so nothing is lost.
var forceMove bool

const previousRefPrefix = "refs/remote-sync/previous/"

// droppedTips parses the "D:<sha>" lines printed by the guard scripts.
func droppedTips(out string) []string {
	var tips []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "D:") && shaRe.MatchString(line[2:]) && !seen[line[2:]] {
			seen[line[2:]] = true
			tips = append(tips, line[2:])
		}
	}
	return tips
}

// guardFailure builds the message for commits that would become unreachable.
func guardFailure(side string, dropped []string, log string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "the %s branch has commits that are not in the new history and would be dropped (not a fast-forward):\n", side)
	sb.WriteString(strings.TrimRight(log, "\n"))
	sb.WriteString("\nUse -direction merge to combine both histories, or -force to overwrite (the old tip is saved under " + previousRefPrefix + "<sha>).")
	return fmt.Errorf("%s", sb.String())
}

// guardRemoteMove checks that moving the remote branch (and HEAD) to target
// loses no commits. target must already exist on the remote.
func guardRemoteMove(remote *RemoteConfig, branch, target string) error {
	script := fmt.Sprintf("( for r in HEAD refs/heads/%s; do t=$(git rev-parse -q --verify $r 2>/dev/null) || continue; "+
		"if ! git merge-base --is-ancestor $t %s 2>/dev/null; then echo D:$t; fi; done )", shellQuote(branch), target)
	out, err := remoteRun(remote, script)
	if err != nil {
		return fmt.Errorf("remote fast-forward check failed: %w", err)
	}
	dropped := droppedTips(out)
	if len(dropped) == 0 {
		return nil
	}
	log, _ := remoteRun(remote, fmt.Sprintf("git log --oneline -n 10 %s --not %s 2>/dev/null", strings.Join(dropped, " "), target))
	if !forceMove {
		return guardFailure("remote", dropped, log)
	}
	fmt.Printf("⚠ Remote history is overwritten; old tip(s) kept as %s<sha>:\n%s\n", previousRefPrefix, strings.TrimRight(log, "\n"))
	for _, d := range dropped {
		if out, err := remoteRun(remote, fmt.Sprintf("git update-ref %s%s %s && echo OK", previousRefPrefix, d, d)); err != nil || !strings.Contains(out, "OK") {
			return fmt.Errorf("could not save old remote tip %s: %v", d, err)
		}
	}
	return nil
}

// guardLocalMove is guardRemoteMove for the local repository.
func guardLocalMove(gitRoot, branch, target string) error {
	var dropped []string
	for _, r := range []string{"HEAD", "refs/heads/" + branch} {
		out, err := localGit(gitRoot, "rev-parse", "-q", "--verify", r)
		t := strings.TrimSpace(out)
		if err != nil || !shaRe.MatchString(t) {
			continue
		}
		if _, err := localGit(gitRoot, "merge-base", "--is-ancestor", t, target); err != nil {
			dropped = append(dropped, t)
		}
	}
	dropped = droppedTips(strings.Join(prefixAll("D:", dropped), "\n"))
	if len(dropped) == 0 {
		return nil
	}
	args := append([]string{"log", "--oneline", "-n", "10"}, dropped...)
	args = append(args, "--not", target)
	log, _ := localGit(gitRoot, args...)
	if !forceMove {
		return guardFailure("local", dropped, log)
	}
	fmt.Printf("⚠ Local history is overwritten; old tip(s) kept as %s<sha>:\n%s\n", previousRefPrefix, strings.TrimRight(log, "\n"))
	for _, d := range dropped {
		if out, err := localGitCombined(gitRoot, "update-ref", previousRefPrefix+d, d); err != nil {
			return fmt.Errorf("could not save old local tip %s: %v %s", d, err, out)
		}
	}
	return nil
}

func prefixAll(p string, in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = p + s
	}
	return out
}
