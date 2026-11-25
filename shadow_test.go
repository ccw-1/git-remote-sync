package main

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLooksLikeText(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"empty", nil, true},
		{"ascii", []byte("hello world\n\tmore\n"), true},
		{"utf8", []byte("grüße, 世界\n"), true},
		{"latin1", []byte{0x48, 0xe9, 0x6c, 0x6c, 0x6f, '\n'}, true},
		{"nul", []byte("abc\x00def"), false},
		{"control", []byte{0x01, 0x02, 0x03, 0xff}, false},
	}
	for _, c := range cases {
		if got := looksLikeText(c.in); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSplitUstarName(t *testing.T) {
	if p, r, ok := splitUstarName("a/b/c"); !ok || p != "" || r != "a/b/c" {
		t.Fatalf("short name: %q %q %v", p, r, ok)
	}
	long := strings.Repeat("x", 60) + "/" + strings.Repeat("y", 60) + "/file"
	p, r, ok := splitUstarName(long)
	if !ok || len(p) > 155 || len(r) > 100 || p+"/"+r != long {
		t.Fatalf("long name not split: %q %q %v", p, r, ok)
	}
	if _, _, ok := splitUstarName(strings.Repeat("z", 200)); ok {
		t.Fatal("200 byte single component must not fit ustar")
	}
	// directory names keep their trailing slash in the name field
	if _, r, ok := splitUstarName(long + "dir/"); !ok || !strings.HasSuffix(r+"/", "/") {
		t.Fatalf("directory name: %q %v", r, ok)
	}
}

// makeShadowTree builds a small repository-like tree for the archive tests.
func makeShadowTree(t *testing.T) (root string, longRel string) {
	t.Helper()
	root = t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, ".git", "objects"), 0755))
	must(os.MkdirAll(filepath.Join(root, "src", "deep"), 0755))
	must(os.WriteFile(filepath.Join(root, "README.md"), []byte("# hello\n"), 0644))
	must(os.WriteFile(filepath.Join(root, "src", "deep", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0755))
	must(os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n\tbare = false\n"), 0644))
	must(os.WriteFile(filepath.Join(root, ".git", "objects", "pack.bin"), []byte{0, 1, 2, 0xff, 0xfe}, 0644))
	must(os.WriteFile(filepath.Join(root, ".git", "index.lock"), []byte("stale"), 0644))
	must(os.WriteFile(filepath.Join(root, remoteSyncFile), []byte("remote-path:me@host:/x\n"), 0644))
	must(os.Symlink("README.md", filepath.Join(root, "link.md")))

	longRel = filepath.ToSlash(filepath.Join(
		".git", "objects", strings.Repeat("d", 70), strings.Repeat("e", 70), "blob"))
	must(os.MkdirAll(filepath.Join(root, filepath.FromSlash(filepath.Dir(longRel))), 0755))
	must(os.WriteFile(filepath.Join(root, filepath.FromSlash(longRel)), []byte("long path\n"), 0644))
	return root, longRel
}

func TestWriteTreeArchiveTarRoundTrip(t *testing.T) {
	root, longRel := makeShadowTree(t)

	var buf bytes.Buffer
	st, err := writeTreeArchive(&buf, root, false, nil)
	if err != nil {
		t.Fatalf("writeTreeArchive: %v", err)
	}
	if st.Files != 5 {
		t.Fatalf("expected 5 regular files, got %d", st.Files)
	}

	// Inspect the raw entries for the things extractArchive ignores.
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	sawDir, sawLink := false, false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading archive: %v", err)
		}
		switch {
		case hdr.Typeflag == tar.TypeDir && strings.TrimSuffix(hdr.Name, "/") == "src/deep":
			sawDir = true
		case hdr.Typeflag == tar.TypeSymlink && hdr.Name == "link.md":
			if hdr.Linkname != "README.md" {
				t.Fatalf("symlink target %q", hdr.Linkname)
			}
			sawLink = true
		case hdr.Name == remoteSyncFile:
			t.Fatalf("%s must not be in the archive", remoteSyncFile)
		case strings.HasSuffix(hdr.Name, ".lock"):
			t.Fatalf("%s must not be in the archive", hdr.Name)
		}
	}
	if !sawDir || !sawLink {
		t.Fatalf("missing dir (%v) or symlink (%v) entry", sawDir, sawLink)
	}

	dest := t.TempDir()
	if err := extractArchive(bytes.NewReader(buf.Bytes()), dest, nil); err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "src", "deep", "run.sh"))
	if err != nil || string(got) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("run.sh content: %q %v", got, err)
	}
	if fi, err := os.Stat(filepath.Join(dest, "src", "deep", "run.sh")); err != nil || fi.Mode()&0111 == 0 {
		t.Fatalf("executable bit lost: %v %v", fi, err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(longRel))); err != nil || string(got) != "long path\n" {
		t.Fatalf("long path content: %q %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, ".git", "objects", "pack.bin")); err != nil || !bytes.Equal(got, []byte{0, 1, 2, 0xff, 0xfe}) {
		t.Fatalf("binary content: %v %v", got, err)
	}
}

func TestWriteTreeArchiveZOSTags(t *testing.T) {
	root, longRel := makeShadowTree(t)

	var buf bytes.Buffer
	if _, err := writeTreeArchive(&buf, root, true, nil); err != nil {
		t.Fatalf("writeTreeArchive: %v", err)
	}

	// taginfo per regular file: text 1 819, binary 0 65535
	tags := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading archive: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.ToSlash(hdr.Name)
		if _, err := io.Copy(io.Discard, tr); err != nil {
			t.Fatal(err)
		}
		tags[name] = hdr.PAXRecords["ZOS.taginfo"]
	}
	for name, want := range map[string]string{
		"README.md":               "1 819",
		"src/deep/run.sh":         "1 819",
		".git/config":             "1 819",
		".git/objects/pack.bin":   "0 65535",
		filepath.ToSlash(longRel): "1 819",
	} {
		if got := tags[name]; got != want {
			t.Errorf("%s: taginfo %q, want %q", name, got, want)
		}
	}

	dest := t.TempDir()
	if err := extractArchive(bytes.NewReader(buf.Bytes()), dest, nil); err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, ".git", "objects", "pack.bin")); err != nil || !bytes.Equal(got, []byte{0, 1, 2, 0xff, 0xfe}) {
		t.Fatalf("binary content after tagged round trip: %v %v", got, err)
	}
}

func TestSkipSeedPath(t *testing.T) {
	skip := []string{remoteSyncFile, ".git/index.lock", ".git/refs/heads/main.lock", ".git/gc.pid", ".git/MERGE_RR", ".git/rebase-merge", ".git/BISECT_LOG"}
	for _, p := range skip {
		if !skipSeedPath(p) {
			t.Errorf("%s should be skipped", p)
		}
	}
	keep := []string{".git/index", ".git/config", "src/main.go", "notes.lock"}
	for _, p := range keep {
		if skipSeedPath(p) {
			t.Errorf("%s should be kept", p)
		}
	}
}
