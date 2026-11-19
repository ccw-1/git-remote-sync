package main

// File transfer over the SSH connection only.
//
// The remote repositories are air-gapped: the only way in is our own SSH
// session, so git objects can no longer be fetched from origin by the remote.
// Missing commits are shipped as a git bundle through this code, in both
// directions.
//
// On z/OS the bundle is binary data and must arrive byte for byte with a
// binary file tag. The approach is borrowed from z-scp (../z-scp): files
// travel as pax archives ("pax -r" / "pax -w" with a ZOS.taginfo extended
// header) and every byte stream is translated to cancel the EBCDIC/ASCII
// translation that z/OS applies at the SSH boundary when _BPXK_AUTOCVT=ON:
//   - upload:   e2a[] is applied before the bytes enter ssh (z/OS applies a2e[]
//     to stdin, so the remote process sees the original bytes)
//   - download: a2e[] is applied to what comes out of ssh (z/OS applied e2a[]
//     to the stdout of the remote process)

import (
	"archive/tar"
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// e2a is the EBCDIC-1047 -> ISO-8859-1 table (same table as z-scp).
var e2a = [256]byte{
	0x00, 0x01, 0x02, 0x03, 0x9c, 0x09, 0x86, 0x7f, 0x97, 0x8d, 0x8e, 0x0b,
	0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x9d, 0x0a, 0x08, 0x87,
	0x18, 0x19, 0x92, 0x8f, 0x1c, 0x1d, 0x1e, 0x1f, 0x80, 0x81, 0x82, 0x83,
	0x84, 0x85, 0x17, 0x1b, 0x88, 0x89, 0x8a, 0x8b, 0x8c, 0x05, 0x06, 0x07,
	0x90, 0x91, 0x16, 0x93, 0x94, 0x95, 0x96, 0x04, 0x98, 0x99, 0x9a, 0x9b,
	0x14, 0x15, 0x9e, 0x1a, 0x20, 0xa0, 0xe2, 0xe4, 0xe0, 0xe1, 0xe3, 0xe5,
	0xe7, 0xf1, 0xa2, 0x2e, 0x3c, 0x28, 0x2b, 0x7c, 0x26, 0xe9, 0xea, 0xeb,
	0xe8, 0xed, 0xee, 0xef, 0xec, 0xdf, 0x21, 0x24, 0x2a, 0x29, 0x3b, 0x5e,
	0x2d, 0x2f, 0xc2, 0xc4, 0xc0, 0xc1, 0xc3, 0xc5, 0xc7, 0xd1, 0xa6, 0x2c,
	0x25, 0x5f, 0x3e, 0x3f, 0xf8, 0xc9, 0xca, 0xcb, 0xc8, 0xcd, 0xce, 0xcf,
	0xcc, 0x60, 0x3a, 0x23, 0x40, 0x27, 0x3d, 0x22, 0xd8, 0x61, 0x62, 0x63,
	0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0xab, 0xbb, 0xf0, 0xfd, 0xfe, 0xb1,
	0xb0, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f, 0x70, 0x71, 0x72, 0xaa, 0xba,
	0xe6, 0xb8, 0xc6, 0xa4, 0xb5, 0x7e, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78,
	0x79, 0x7a, 0xa1, 0xbf, 0xd0, 0x5b, 0xde, 0xae, 0xac, 0xa3, 0xa5, 0xb7,
	0xa9, 0xa7, 0xb6, 0xbc, 0xbd, 0xbe, 0xdd, 0xa8, 0xaf, 0x5d, 0xb4, 0xd7,
	0x7b, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0xad, 0xf4,
	0xf6, 0xf2, 0xf3, 0xf5, 0x7d, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50,
	0x51, 0x52, 0xb9, 0xfb, 0xfc, 0xf9, 0xfa, 0xff, 0x5c, 0xf7, 0x53, 0x54,
	0x55, 0x56, 0x57, 0x58, 0x59, 0x5a, 0xb2, 0xd4, 0xd6, 0xd2, 0xd3, 0xd5,
	0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0xb3, 0xdb,
	0xdc, 0xd9, 0xda, 0x9f,
}

// a2e is the inverse of e2a (ISO-8859-1 -> EBCDIC-1047). e2a is a permutation,
// which is what makes the wire translation lossless for arbitrary binary data.
var a2e [256]byte

func init() {
	for i, b := range e2a {
		a2e[b] = byte(i)
	}
}

// translateWriter translates every byte with a table before passing it on.
type translateWriter struct {
	w   io.Writer
	tab *[256]byte
}

func (c translateWriter) Write(p []byte) (int, error) {
	buf := make([]byte, len(p))
	for i, b := range p {
		buf[i] = c.tab[b]
	}
	return c.w.Write(buf)
}

// translateReader translates every byte read with a table.
type translateReader struct {
	r   io.Reader
	tab *[256]byte
}

func (c translateReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	for i := 0; i < n; i++ {
		p[i] = c.tab[p[i]]
	}
	return n, err
}

// ---------------------------------------------------------------------------
// Remote probe: z/OS detection and fully qualified tool paths
// ---------------------------------------------------------------------------

// remoteToolNames are the tools run on the remote by their fully qualified
// path. On z/OS the PATH after the setup command usually prefers other
// versions of these tools (e.g. zopen), so the native ones are addressed
// explicitly. Depending on the system they live in /bin or in /usr/bin.
var remoteToolNames = []string{"cat", "rm", "mkdir", "ln", "pax", "find", "touch", "chtag", "iconv"}

// probeRemote determines whether the remote is z/OS and where the tools are.
func probeRemote(remote *RemoteConfig) error {
	// Only z/OS needs the fully qualified tools; any other POSIX system
	// is served by plain tar/cat found on PATH.
	script := `if [ "$(uname)" = "OS/390" ]; then echo OS=ZOS; ` +
		`for t in ` + strings.Join(remoteToolNames, " ") + `; do ` +
		`for d in /bin /usr/bin; do if [ -x $d/$t ]; then echo T:$t=$d/$t; break; fi; done; done; ` +
		`else echo OS=UNIX; fi`
	remoteCmd := buildRemoteCommand(remote, "( "+script+" )")
	logCommand("R", script)
	output, err := exec.Command("ssh", getSSHTarget(remote), remoteCmd).Output()
	if err != nil {
		return fmt.Errorf("remote probe failed: %w", err)
	}
	remote.Tools = make(map[string]string)
	sawOS := false
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "OS=ZOS":
			remote.IsZOS, sawOS = true, true
		case line == "OS=UNIX":
			remote.IsZOS, sawOS = false, true
		case strings.HasPrefix(line, "T:"):
			if kv := strings.SplitN(line[2:], "=", 2); len(kv) == 2 {
				remote.Tools[kv[0]] = kv[1]
			}
		}
	}
	if !sawOS {
		return fmt.Errorf("remote probe returned no OS information: %q", strings.TrimSpace(string(output)))
	}
	remote.Probed = true
	return nil
}

// rt returns the fully qualified path of a remote tool (bare name when the
// probe did not find it).
func rt(remote *RemoteConfig, name string) string {
	if p, ok := remote.Tools[name]; ok {
		return p
	}
	return name
}

// ---------------------------------------------------------------------------
// Upload: local file -> remote file
// ---------------------------------------------------------------------------

// paxXhdrRecord builds one extended header record "LEN KEY=VALUE\n" where LEN
// counts the whole record including its own digits.
func paxXhdrRecord(key, val string) string {
	body := " " + key + "=" + val + "\n"
	for l := len(body) + 1; ; l++ {
		s := fmt.Sprintf("%d%s", l, body)
		if len(s) == l {
			return s
		}
	}
}

// paxHeader builds one 512-byte ustar header. The checksum is computed on the
// logical bytes (z-scp: remote pax sees the original bytes after AUTOCVT, so
// the wire translation must not enter the checksum).
func paxHeader(name string, size int64, mtime int64, typeflag byte, mode uint32) ([]byte, error) {
	if len(name) > 100 {
		return nil, fmt.Errorf("name too long for ustar: %s", name)
	}
	h := make([]byte, 512)
	copy(h, name)
	copy(h[100:], fmt.Sprintf("%07o", mode&07777))
	copy(h[108:], "0000000")
	copy(h[116:], "0000000")
	copy(h[124:], fmt.Sprintf("%011o", size))
	copy(h[136:], fmt.Sprintf("%011o", mtime))
	h[156] = typeflag
	copy(h[257:], "ustar\x0000")
	for i := 148; i < 156; i++ {
		h[i] = ' '
	}
	sum := 0
	for _, b := range h {
		sum += int(b)
	}
	copy(h[148:], fmt.Sprintf("%06o", sum))
	h[154] = 0
	h[155] = ' '
	return h, nil
}

func writePadding(w io.Writer, n int64) error {
	if n%512 == 0 {
		return nil
	}
	_, err := w.Write(make([]byte, 512-n%512))
	return err
}

// writePaxFile writes a complete pax archive holding the single local file as
// arcName with the binary tag (ZOS.taginfo=0 65535), mode 0600.
func writePaxFile(w io.Writer, localPath, arcName string) error {
	return writeArchiveFile(w, localPath, arcName, true)
}

func writeArchiveFile(w io.Writer, localPath, arcName string, zosTag bool) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", localPath)
	}
	size := st.Size()
	mtime := time.Now().Unix()

	if zosTag {
		xdata := paxXhdrRecord("ZOS.taginfo", "0 65535")
		xh, err := paxHeader("PaxHeader/"+path.Base(arcName), int64(len(xdata)), mtime, 'x', 0600)
		if err != nil {
			return err
		}
		if _, err := w.Write(xh); err != nil {
			return err
		}
		if _, err := io.WriteString(w, xdata); err != nil {
			return err
		}
		if err := writePadding(w, int64(len(xdata))); err != nil {
			return err
		}
	}

	fh, err := paxHeader(arcName, size, mtime, '0', 0600)
	if err != nil {
		return err
	}
	if _, err := w.Write(fh); err != nil {
		return err
	}
	// Stream at most size bytes so the archive stays consistent even if the
	// file changes while we read it.
	n, err := io.Copy(w, io.LimitReader(f, size))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("%s shrank during upload", localPath)
	}
	if err := writePadding(w, size); err != nil {
		return err
	}
	// End of archive: two zero blocks
	_, err = w.Write(make([]byte, 1024))
	return err
}

// writeTarFile writes a plain ustar archive holding the single local file as
// arcName (used for non-z/OS remotes; z/OS gets the tagged pax variant).
func writeTarFile(w io.Writer, localPath, arcName string) error {
	return writeArchiveFile(w, localPath, arcName, false)
}

// uploadBinaryFile copies a local file to relPath (relative to the remote
// repository directory) over SSH, byte for byte. z/OS remotes get the file
// as a pax archive with a binary tag; other POSIX remotes get a plain tar.
func uploadBinaryFile(remote *RemoteConfig, localPath, relPath string) error {
	var remoteCmd string
	if remote.IsZOS {
		remoteCmd = buildRemoteCommand(remote, "_BPXK_AUTOCVT=ON "+rt(remote, "pax")+" -r -x pax -p p")
	} else {
		remoteCmd = buildRemoteCommand(remote, "tar -xf -")
	}
	logCommand("R", remoteCmd)

	cmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
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

	var werr error
	if remote.IsZOS {
		werr = writePaxFile(translateWriter{stdin, &e2a}, localPath, relPath)
	} else {
		werr = writeTarFile(stdin, localPath, relPath)
	}
	stdin.Close()
	err = cmd.Wait()
	if werr != nil {
		return fmt.Errorf("sending %s: %v (ssh output: %s)", relPath, werr, strings.TrimSpace(out.String()))
	}
	if err != nil {
		return fmt.Errorf("remote receive of %s failed: %v\nOutput: %s", relPath, err, strings.TrimSpace(out.String()))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Download: remote files -> local directory
// ---------------------------------------------------------------------------

// safeRelPath rejects archive names that would leave the destination tree.
func safeRelPath(name string) (string, bool) {
	clean := path.Clean(name)
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// extractArchive writes the regular files of a tar/pax stream below destRoot.
// Files tagged EBCDIC-1047 text by z/OS pax (ZOS.taginfo=1 1047) are converted
// to ISO-8859-1, as z-scp does. Modes (executable bits) and mtimes are kept.
func extractArchive(r io.Reader, destRoot string, onFile func(name string)) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // directories, global headers, links
		}
		rel, ok := safeRelPath(hdr.Name)
		if !ok {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		dest := filepath.Join(destRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		// Replace atomically-ish: remove first so a read-only or symlinked
		// destination cannot redirect the write.
		os.Remove(dest)
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(hdr.Mode&0777)|0600)
		if err != nil {
			return err
		}
		var src io.Reader = tr
		if hdr.PAXRecords["ZOS.taginfo"] == "1 1047" {
			src = translateReader{tr, &e2a}
		}
		_, err = io.Copy(f, src)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("writing %s: %w", rel, err)
		}
		if hdr.Mode&0111 == 0 {
			os.Chmod(dest, 0644)
		}
		if !hdr.ModTime.IsZero() {
			os.Chtimes(dest, hdr.ModTime, hdr.ModTime)
		}
		if onFile != nil {
			onFile(rel)
		}
	}
}

// downloadFiles fetches files (paths relative to the remote repository
// directory) into destRoot over SSH. Batched to keep the command line short.
func downloadFiles(remote *RemoteConfig, destRoot string, files []string, onFile func(name string)) error {
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
		var remoteCmd string
		if remote.IsZOS {
			remoteCmd = buildRemoteCommand(remote, "_BPXK_AUTOCVT=ON "+rt(remote, "pax")+" -w -x pax "+strings.Join(quoted, " "))
		} else {
			remoteCmd = buildRemoteCommand(remote, "tar -cf - "+strings.Join(quoted, " "))
		}
		logCommand("R", remoteCmd)

		cmd := exec.Command("ssh", getSSHTarget(remote), remoteCmd)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		var r io.Reader = bufio.NewReaderSize(stdout, 1<<16)
		if remote.IsZOS {
			r = translateReader{r, &a2e}
		}
		xerr := extractArchive(r, destRoot, onFile)
		io.Copy(io.Discard, r) // let the remote finish (trailing zero blocks)
		werr := cmd.Wait()
		if xerr != nil {
			return fmt.Errorf("download failed: %v (ssh stderr: %s)", xerr, strings.TrimSpace(stderr.String()))
		}
		if werr != nil {
			return fmt.Errorf("remote archive command failed: %v (stderr: %s)", werr, strings.TrimSpace(stderr.String()))
		}
	}
	return nil
}
