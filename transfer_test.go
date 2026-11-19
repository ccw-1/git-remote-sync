package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteTarFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.bin")
	data := bytes.Repeat([]byte{0, 1, 2, 0xff, 'a'}, 300)
	if err := os.WriteFile(src, data, 0644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeTarFile(&buf, src, "sub/out.bin"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "dest")
	if err := extractArchive(&buf, dest, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "sub", "out.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("round trip mismatch: %v", err)
	}
}

func TestWritePaxFileHasTag(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.bin")
	os.WriteFile(src, []byte("hello"), 0644)
	var buf bytes.Buffer
	if err := writePaxFile(&buf, src, "x.bin"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("ZOS.taginfo=0 65535")) {
		t.Fatal("missing ZOS.taginfo record")
	}
	dest := filepath.Join(dir, "d")
	if err := extractArchive(&buf, dest, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "x.bin")); string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestE2APermutation(t *testing.T) {
	seen := map[byte]bool{}
	for i := 0; i < 256; i++ {
		seen[e2a[i]] = true
		if a2e[e2a[i]] != byte(i) {
			t.Fatalf("a2e not inverse at %d", i)
		}
	}
	if len(seen) != 256 {
		t.Fatal("e2a is not a permutation")
	}
}

func TestParseDirection(t *testing.T) {
	for in, want := range map[string]Direction{"l2r": dirLocalToRemote, "push": dirLocalToRemote, "r2l": dirRemoteToLocal, "pull": dirRemoteToLocal, "merge": dirMerge} {
		got, err := parseDirection(in)
		if err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := parseDirection("bogus"); err == nil {
		t.Error("expected error")
	}
}

func TestDroppedTips(t *testing.T) {
	a := strings.Repeat("a", 40)
	b := strings.Repeat("b", 40)
	got := droppedTips("noise\nD:" + a + "\nD:" + a + "\nD:" + b + "\nD:xyz\n")
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("got %v", got)
	}
}
