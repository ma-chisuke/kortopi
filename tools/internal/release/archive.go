package release

import (
	"archive/tar"
	"archive/zip"
	"compress/flate"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Entry is a file to put into an archive.
type Entry struct {
	// Name is the path of the file in the archive, below its directory.
	Name string
	// Source is the file on disk.
	Source string
}

// The modes of the files in an archive: what Git records, executable or not.
const (
	modeDir  = 0o755
	modeExec = 0o755
	modeFile = 0o644
)

// entryMode returns the mode that a file gets in an archive: executable when
// any execute bit is set on disk, so that the umask of the machine that built
// it does not matter.
func entryMode(info os.FileInfo) os.FileMode {
	if info.Mode()&0o111 != 0 {
		return modeExec
	}
	return modeFile
}

// sorted returns the entries ordered by name, so that the order of the
// arguments does not change the archive.
func sorted(entries []Entry) []Entry {
	s := slices.Clone(entries)
	slices.SortFunc(s, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	return s
}

// WriteArchive writes the entries into the archive at out, under the
// directory dir, as a gzipped tar file or a zip file, by the extension of
// out. Every time in the archive is mtime, the owner is root, and a mode is
// 0755 or 0644, so the same files always give the same bytes.
func WriteArchive(out, dir string, entries []Entry, mtime time.Time) (err error) {
	var write func(io.Writer, string, []Entry, time.Time) error
	switch {
	case strings.HasSuffix(out, ".tar.gz"):
		write = WriteTarGz
	case strings.HasSuffix(out, ".zip"):
		write = WriteZip
	default:
		return fmt.Errorf("%s: an archive ends in .tar.gz or .zip", out)
	}
	f, err := os.Create(filepath.Clean(out))
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	return write(f, dir, entries, mtime)
}

// WriteTarGz writes the entries as a gzipped tar file under the directory dir.
func WriteTarGz(w io.Writer, dir string, entries []Entry, mtime time.Time) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	// No name and no time in the gzip header.
	tw := tar.NewWriter(gz)
	mtime = mtime.UTC().Truncate(time.Second)
	header := func(name string, mode int64, size int64, typ byte) *tar.Header {
		return &tar.Header{
			Typeflag: typ,
			Name:     name,
			Mode:     mode,
			Size:     size,
			ModTime:  mtime,
		}
	}
	if err := tw.WriteHeader(header(dir+"/", modeDir, 0, tar.TypeDir)); err != nil {
		return err
	}
	for _, e := range sorted(entries) {
		if err := addTar(tw, header, dir, e); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func addTar(tw *tar.Writer, header func(string, int64, int64, byte) *tar.Header, dir string, e Entry) error {
	f, err := os.Open(e.Source)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(header(dir+"/"+e.Name, int64(entryMode(info)), info.Size(), tar.TypeReg)); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// WriteZip writes the entries as a zip file under the directory dir.
func WriteZip(w io.Writer, dir string, entries []Entry, mtime time.Time) error {
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	mtime = mtime.UTC().Truncate(time.Second)
	dh := &zip.FileHeader{Name: dir + "/", Method: zip.Store, Modified: mtime}
	dh.SetMode(os.ModeDir | modeDir)
	if _, err := zw.CreateHeader(dh); err != nil {
		return err
	}
	for _, e := range sorted(entries) {
		if err := addZip(zw, dir, e, mtime); err != nil {
			return err
		}
	}
	return zw.Close()
}

func addZip(zw *zip.Writer, dir string, e Entry, mtime time.Time) error {
	f, err := os.Open(e.Source)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	h := &zip.FileHeader{Name: dir + "/" + e.Name, Method: zip.Deflate, Modified: mtime}
	h.SetMode(entryMode(info))
	zf, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(zf, f)
	return err
}

// WriteChecksums writes the SHA-256 of each file, one line per file in the
// format of sha256sum ("<hex>  <name>"), with the base names of the files in
// sorted order.
func WriteChecksums(w io.Writer, files []string) error {
	lines := make([]string, 0, len(files))
	for _, p := range files {
		sum, err := sha256File(p)
		if err != nil {
			return err
		}
		lines = append(lines, sum+"  "+filepath.Base(p)+"\n")
	}
	slices.SortFunc(lines, func(a, b string) int { return strings.Compare(a[66:], b[66:]) })
	_, err := io.WriteString(w, strings.Join(lines, ""))
	return err
}

func sha256File(p string) (string, error) {
	f, err := os.Open(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
