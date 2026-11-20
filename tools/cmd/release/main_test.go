package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runRelease runs the command and returns its exit status and stderr; it
// prints nothing on stdout without findings.
func runRelease(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	noEnv := func(context.Context, ...string) ([]string, error) {
		t.Fatal("the command asked for go env")
		return nil, nil
	}
	code := run(context.Background(), args, &stdout, &stderr, noEnv)
	if stdout.Len() > 0 {
		t.Errorf("release %q printed %q", args, stdout.String())
	}
	return code, stderr.String()
}

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"publish"},
		{"notices", "-o", "x"},
		{"archive", "-o", "a.tar.gz", "-dir", "a", "-mtime", "soon", "kortopi=kortopi"},
		{"archive", "-o", "a.tar.gz", "-dir", "a", "-mtime", "0", "sub/kortopi=kortopi"},
		{"checksums", "-o", "sums.txt"},
		{"checksums", "-unknown"},
	} {
		if code, stderr := runRelease(t, args...); code != exitError || stderr == "" {
			t.Errorf("release %q: status %d and stderr %q, want %d and a message", args, code, stderr, exitError)
		}
	}
	if code, stderr := runRelease(t, "archive", "-h"); code != exitOK || !strings.Contains(stderr, "usage:") {
		t.Errorf("release archive -h: status %d and stderr %q, want the usage", code, stderr)
	}
}

func TestRunArchiveAndChecksums(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "kortopi")
	if err := os.WriteFile(bin, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "kortopi_v1.2.3_linux_amd64.tar.gz")
	if code, stderr := runRelease(t, "archive", "-o", archive, "-dir", "kortopi_v1.2.3_linux_amd64", "-mtime", "1763604782", "kortopi="+bin); code != exitOK {
		t.Fatalf("release archive: status %d, stderr %q", code, stderr)
	}
	sums := filepath.Join(dir, "sums.txt")
	if code, stderr := runRelease(t, "checksums", "-o", sums, archive); code != exitOK {
		t.Fatalf("release checksums: status %d, stderr %q", code, stderr)
	}
	b, err := os.ReadFile(filepath.Clean(sums))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "  kortopi_v1.2.3_linux_amd64.tar.gz\n") || len(b) != 64+2+len("kortopi_v1.2.3_linux_amd64.tar.gz")+1 {
		t.Errorf("the checksum file is %q", b)
	}
}
