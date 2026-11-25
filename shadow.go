package main

// Shadow seeding: when the remote repository directory does not exist, the
// local repository (working tree and .git) is streamed to the remote as one
// pax (z/OS) or tar (other POSIX) archive. The remote cannot clone from
// anywhere, so this one-time copy is what bootstraps a new remote checkout.
//
// On z/OS every regular file carries a ZOS.taginfo extended header, as in
// z-scp's recursive upload: text files get CCSID 819 (tag on, content left as
// ISO-8859-1 bytes), everything else gets CCSID 65535 (binary, tag off). The
// bytes themselves never change, so git blobs and the copied index still
// agree and the first sync finds nothing to transfer.

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// seedSampleSize is how much of a file is inspected to choose the z/OS tag.
// A wrong choice only changes the tag, never the bytes.
const seedSampleSize = 64 * 1024

// treeStats summarises a shadow archive.
type treeStats struct {
	Files int
	Bytes int64
}

// seedSkipExact lists git files that hold transient state of an operation in
// progress. They must not travel: the remote copy would look like a merge,
// rebase or bisect is running, and the following git commands would refuse or
// act on that state.
var seedSkipExact = map[string]bool{
	remoteSyncFile:                 true,
	".git/MERGE_HEAD":              true,
	".git/MERGE_MSG":               true,
	".git/MERGE_MODE":              true,
	".git/MERGE_RR":                true,
	".git/AUTO_MERGE":              true,
	".git/CHERRY_PICK_HEAD":        true,
	".git/REVERT_HEAD":             true,
	".git/REBASE_HEAD":             true,
	".git/SQUASH_MSG":              true,
	".git/COMMIT_EDITMSG":          true,
	".git/TAG_EDITMSG":             true,
	".git/BISECT_LOG":              true,
	".git/BISECT_START":            true,
	".git/BISECT_TERMS":            true,
	".git/gc.pid":                  true,
	".git/gc.log":                  true,
	".git/remote-sync.bundle":      true,
	".git/objects/info/alternates": true,
}

// seedSkipDir lists git directories that describe local state only.
var seedSkipDir = map[string]bool{
	".git/rebase-merge": true,
	".git/rebase-apply": true,
	".git/sequencer":    true,
	".git/worktrees":    true,
}

// skipSeedPath reports paths that must not travel with the shadow: the local
// connection settings, git lock files and the state of an operation in
// progress.
func skipSeedPath(rel string) bool {
	if seedSkipExact[rel] || seedSkipDir[rel] {
		return true
	}
	if strings.HasPrefix(rel, ".git/") {
		if strings.HasSuffix(rel, ".lock") {
			return true
		}
		// BISECT_* siblings (BISECT_EXPECTED_REV, BISECT_ANCESTORS_OK, ...)
		base := rel[strings.LastIndex(rel, "/")+1:]
		if strings.HasPrefix(base, "BISECT_") {
			return true
		}
	}
	return false
}

// looksLikeText applies the z/OS tagging rule to a file sample: no NUL bytes
// and either valid UTF-8 or printable Latin-1. Text is stored with the
// ISO-8859-1 tag, everything else as binary.
func looksLikeText(sample []byte) bool {
	if len(sample) == 0 {
		return true
	}
	for _, b := range sample {
		if b == 0 {
			return false
		}
	}
	if utf8.Valid(sample) {
		return true
	}
	for _, b := range sample {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' && b != '\f' && b != '\b' {
			return false
		}
	}
	return true
}

// humanBytes formats a byte count for progress output.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// writeEntryHeader writes a pax extended header when needed and then the
// ustar header for one archive entry. records are entry-specific extended
// header records (for example ZOS.taginfo).
func writeEntryHeader(w io.Writer, name string, size, mtime int64, typeflag byte, mode uint32, linkname string, records []paxRecord) error {
	var recs []paxRecord
	if _, _, ok := splitUstarName(name); !ok {
		recs = append(recs, paxRecord{"path", name})
	}
	if len(linkname) > 100 {
		recs = append(recs, paxRecord{"linkpath", linkname})
		linkname = ""
	}
	recs = append(recs, records...)
	if len(recs) > 0 {
		if err := writePaxExt(w, name, recs, mtime, mode); err != nil {
			return err
		}
	}
	hdr, needsPath, err := ustarHeader(name, size, mtime, typeflag, mode, linkname)
	if err != nil {
		return err
	}
	if needsPath && !hasPaxRecord(recs, "path") {
		return fmt.Errorf("name too long for the archive: %s", name)
	}
	_, err = w.Write(hdr)
	return err
}

func hasPaxRecord(records []paxRecord, key string) bool {
	for _, r := range records {
		if r.key == key {
			return true
		}
	}
	return false
}

// writeArchiveRegular writes one regular file with its content.
func writeArchiveRegular(w io.Writer, fullPath, rel string, info fs.FileInfo, zosTags bool) (int64, error) {
	f, err := os.Open(fullPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	size := info.Size()
	mtime := info.ModTime().Unix()
	mode := uint32(info.Mode().Perm())

	var records []paxRecord
	if zosTags {
		tag := "0 65535"
		sample := make([]byte, seedSampleSize)
		n, rerr := io.ReadFull(f, sample)
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			return 0, rerr
		}
		if looksLikeText(sample[:n]) {
			tag = "1 819"
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		records = append(records, paxRecord{"ZOS.taginfo", tag})
	}
	if err := writeEntryHeader(w, rel, size, mtime, '0', mode, "", records); err != nil {
		return 0, err
	}
	copied, err := io.Copy(w, io.LimitReader(f, size))
	if err != nil {
		return 0, err
	}
	if copied != size {
		return 0, fmt.Errorf("%s changed size while it was sent", rel)
	}
	if err := writePadding(w, size); err != nil {
		return 0, err
	}
	return size, nil
}

// writeTreeArchive walks root and writes one pax/tar archive holding the
// whole tree. zosTags selects the z-scp style ZOS.taginfo extended headers.
// Regular files and symbolic links are sent; sockets, fifos and devices are
// skipped. progress is called every 1000 files when it is not nil.
func writeTreeArchive(w io.Writer, root string, zosTags bool, progress func(treeStats)) (treeStats, error) {
	var st treeStats
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if skipSeedPath(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return writeEntryHeader(w, rel+"/", 0, info.ModTime().Unix(), '5', uint32(info.Mode().Perm()), "", nil)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return writeEntryHeader(w, rel, 0, info.ModTime().Unix(), '2', 0777, target, nil)
		case info.Mode().IsRegular():
			n, err := writeArchiveRegular(w, p, rel, info, zosTags)
			if err != nil {
				return err
			}
			st.Files++
			st.Bytes += n
			if progress != nil && st.Files%1000 == 0 {
				progress(st)
			}
			return nil
		default:
			return nil
		}
	})
	if err != nil {
		return st, err
	}
	// End of archive: two zero blocks
	_, err = w.Write(make([]byte, 1024))
	return st, err
}

// seedRemoteRepository creates remote.Path and fills it with a copy of the
// local repository, so the following sync finds both sides identical.
func seedRemoteRepository(gitRoot string, remote *RemoteConfig, stats *SyncStats) error {
	if remote.Path == "" {
		return fmt.Errorf("cannot create the remote repository: no remote path configured")
	}
	gitDir := filepath.Join(gitRoot, ".git")
	if fi, err := os.Stat(gitDir); err != nil {
		return fmt.Errorf("cannot seed the remote repository: %v", err)
	} else if !fi.IsDir() {
		return fmt.Errorf("cannot seed the remote repository: %s is not a directory (linked worktree or submodule?); seed from a normal clone", gitDir)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "objects", "info", "alternates")); err == nil {
		return fmt.Errorf("cannot seed the remote repository: the local repository borrows objects from another one (objects/info/alternates); the copy would not be self-contained")
	}

	target := getSSHTarget(remote)
	fmt.Printf("Creating the remote repository as a shadow copy of %s\n", gitRoot)

	// The archive is extracted into a sibling directory that is renamed into
	// place afterwards: an interrupted seed (Ctrl-C, lost connection) therefore
	// never leaves a half-filled repository behind that later runs would treat
	// as an existing remote.
	parent := path.Dir(remote.Path)
	seedPath := fmt.Sprintf("%s.grs-seed-%d", remote.Path, os.Getpid())

	mkdirCmd := fmt.Sprintf("%s -p %s && %s %s", rt(remote, "mkdir"), parent, rt(remote, "mkdir"), seedPath)
	logCommand("R", mkdirCmd)
	if out, err := sshCommand(target, mkdirCmd).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create %s on the remote: %v (%s)", seedPath, err, strings.TrimSpace(string(out)))
	}

	var remoteCmd string
	if remote.IsZOS {
		remoteCmd = fmt.Sprintf("cd %s && _BPXK_AUTOCVT=ON %s -r -x pax -p p", seedPath, rt(remote, "pax"))
	} else {
		remoteCmd = fmt.Sprintf("cd %s && tar -xf -", seedPath)
	}
	logCommand("R", remoteCmd)

	cmd := sshCommand(target, remoteCmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return err
	}

	var w io.Writer = stdin
	if remote.IsZOS {
		w = translateWriter{stdin, &e2a}
	}
	start := time.Now()
	st, werr := writeTreeArchive(w, gitRoot, remote.IsZOS, func(cur treeStats) {
		fmt.Printf("  ... %d files, %s sent\n", cur.Files, humanBytes(cur.Bytes))
	})
	closeErr := stdin.Close()
	waitErr := cmd.Wait()

	if werr != nil || closeErr != nil || waitErr != nil {
		// Only our own sibling directory is removed: the target did not exist
		// before this run and is still untouched.
		rmCmd := fmt.Sprintf("%s -rf %s", rt(remote, "rm"), seedPath)
		logCommand("R", rmCmd)
		sshCommand(target, rmCmd).Run()
		switch {
		case werr != nil:
			return fmt.Errorf("seeding failed after %d files: %v (remote output: %s)", st.Files, werr, strings.TrimSpace(out.String()))
		case closeErr != nil:
			return fmt.Errorf("seeding failed while closing the archive stream: %v", closeErr)
		default:
			return fmt.Errorf("the remote could not extract the archive: %v\nOutput: %s", waitErr, strings.TrimSpace(out.String()))
		}
	}

	// Move the finished tree into place, unless the target appeared meanwhile.
	moveCmd := fmt.Sprintf("if test -d %s; then echo EXISTS; else %s %s %s && echo OK; fi",
		remote.Path, rt(remote, "mv"), seedPath, remote.Path)
	logCommand("R", moveCmd)
	moveOut, err := sshCommand(target, moveCmd).CombinedOutput()
	moveResult := strings.TrimSpace(string(moveOut))
	if err != nil || !strings.HasSuffix(moveResult, "OK") {
		sshCommand(target, fmt.Sprintf("%s -rf %s", rt(remote, "rm"), seedPath)).Run()
		if strings.Contains(moveResult, "EXISTS") {
			return fmt.Errorf("the remote path appeared while the shadow copy was being created: %s", remote.Path)
		}
		return fmt.Errorf("failed to move the shadow copy into place: %v (%s)", err, moveResult)
	}

	checkCmd := fmt.Sprintf("test -d %s/.git && echo OK || echo NOTGIT", remote.Path)
	logCommand("R", checkCmd)
	checkOut, err := sshCommand(target, checkCmd).Output()
	if err != nil || strings.TrimSpace(string(checkOut)) != "OK" {
		return fmt.Errorf("the shadow copy has no .git directory (%v, %s)", err, strings.TrimSpace(string(checkOut)))
	}

	stats.FilesTransferred += st.Files
	stats.BytesTransferred += st.Bytes
	fmt.Printf("✓ Shadow copy created: %d files, %s in %v\n", st.Files, humanBytes(st.Bytes), time.Since(start).Round(time.Millisecond))
	return nil
}
