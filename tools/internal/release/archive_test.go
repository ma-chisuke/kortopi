package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// files writes a binary and a document into a new directory and returns
// them as entries, in an order other than that of their names.
func files(t *testing.T, binaryMode os.FileMode) []Entry {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "kortopi")
	doc := filepath.Join(dir, "LICENSE")
	if err := os.WriteFile(bin, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, binaryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("license"), 0o600); err != nil {
		t.Fatal(err)
	}
	return []Entry{{Name: "kortopi", Source: bin}, {Name: "LICENSE", Source: doc}}
}

var mtime = time.Date(2026, 11, 20, 2, 13, 2, 0, time.UTC)

func TestWriteTarGz(t *testing.T) {
	var first, second bytes.Buffer
	if err := WriteTarGz(&first, "kortopi_v1.2.3_linux_amd64", files(t, 0o700), mtime); err != nil {
		t.Fatal(err)
	}
	// Other modes on disk, another order, and another time zone give the
	// same bytes.
	entries := files(t, 0o755)
	entries[0], entries[1] = entries[1], entries[0]
	if err := WriteTarGz(&second, "kortopi_v1.2.3_linux_amd64", entries, mtime.In(time.FixedZone("JST", 9*3600))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Error("two archives of the same files differ")
	}

	gz, err := gzip.NewReader(&first)
	if err != nil {
		t.Fatal(err)
	}
	if !gz.ModTime.IsZero() || gz.Name != "" {
		t.Errorf("the gzip header holds the time %v and the name %q, want none", gz.ModTime, gz.Name)
	}
	tr := tar.NewReader(gz)
	var got []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !h.ModTime.Equal(mtime) || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: time %v, owner %d:%d %q:%q; want %v and root", h.Name, h.ModTime, h.Uid, h.Gid, h.Uname, h.Gname, mtime)
		}
		got = append(got, h.Name+" "+h.FileInfo().Mode().String())
	}
	want := "kortopi_v1.2.3_linux_amd64/ drwxr-xr-x," +
		"kortopi_v1.2.3_linux_amd64/LICENSE -rw-r--r--," +
		"kortopi_v1.2.3_linux_amd64/kortopi -rwxr-xr-x"
	if strings.Join(got, ",") != want {
		t.Errorf("the archive holds %q, want %q", strings.Join(got, ","), want)
	}
}

func TestWriteZip(t *testing.T) {
	var first, second bytes.Buffer
	if err := WriteZip(&first, "kortopi_v1.2.3_windows_amd64", files(t, 0o700), mtime); err != nil {
		t.Fatal(err)
	}
	entries := files(t, 0o755)
	entries[0], entries[1] = entries[1], entries[0]
	if err := WriteZip(&second, "kortopi_v1.2.3_windows_amd64", entries, mtime.In(time.FixedZone("JST", 9*3600))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Error("two archives of the same files differ")
	}

	zr, err := zip.NewReader(bytes.NewReader(first.Bytes()), int64(first.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range zr.File {
		if !f.Modified.Equal(mtime) {
			t.Errorf("%s: time %v, want %v", f.Name, f.Modified, mtime)
		}
		got = append(got, f.Name+" "+f.Mode().String())
	}
	want := "kortopi_v1.2.3_windows_amd64/ drwxr-xr-x," +
		"kortopi_v1.2.3_windows_amd64/LICENSE -rw-r--r--," +
		"kortopi_v1.2.3_windows_amd64/kortopi -rwxr-xr-x"
	if strings.Join(got, ",") != want {
		t.Errorf("the archive holds %q, want %q", strings.Join(got, ","), want)
	}
}

func TestWriteArchive(t *testing.T) {
	dir := t.TempDir()
	if err := WriteArchive(filepath.Join(dir, "a.tar.gz"), "a", files(t, 0o755), mtime); err != nil {
		t.Fatal(err)
	}
	if err := WriteArchive(filepath.Join(dir, "a.zip"), "a", files(t, 0o755), mtime); err != nil {
		t.Fatal(err)
	}
	if err := WriteArchive(filepath.Join(dir, "a.tar"), "a", files(t, 0o755), mtime); err == nil {
		t.Error("WriteArchive wrote an archive whose name ends in .tar")
	}
}

func TestWriteChecksums(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"b.zip": "b", "a.tar.gz": "a"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var b strings.Builder
	if err := WriteChecksums(&b, []string{filepath.Join(dir, "b.zip"), filepath.Join(dir, "a.tar.gz")}); err != nil {
		t.Fatal(err)
	}
	want := "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb  a.tar.gz\n" +
		"3e23e8160039594a33894f6564e1b1348bbd7a0088d42c4acb73eeaed59c009d  b.zip\n"
	if b.String() != want {
		t.Errorf("WriteChecksums =\n%s\nwant\n%s", b.String(), want)
	}
}
