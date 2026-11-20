package export

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// staging holds the temporary files of a run, and the directories that it
// created, so that a failure can take them back.
type staging struct {
	dir string
	// created are the directories that the run created, in order.
	created []string
	// temps are the temporary files that have not moved into place.
	temps []string
}

// These modes are what a new file or directory asks for; the umask of the
// user takes its bits away, as for any file that the user creates.
var (
	newFile fs.FileMode = 0o666
	newDir  fs.FileMode = 0o777
)

// write writes the rows of every job to a temporary file in the directory
// of its file, and creates the directories that are missing.
func (s *staging) write(ctx context.Context, db Database, jobs []*job) error {
	for _, j := range jobs {
		if err := s.mkdirs(filepath.Dir(j.path)); err != nil {
			return fmt.Errorf("create the directory of %s: %w", j.path, err)
		}
		if err := s.writeTemp(ctx, db, j); err != nil {
			return err
		}
	}
	return nil
}

// mkdirs creates dir and its parents that are missing, and remembers each.
func (s *staging) mkdirs(dir string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		fi, err := os.Stat(d)
		if err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("%s is not a directory", d)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for _, d := range slices.Backward(missing) {
		if err := os.Mkdir(d, newDir); err != nil {
			return err
		}
		s.created = append(s.created, d)
	}
	return nil
}

// writeTemp writes the rows of one job to a new temporary file next to its
// file. The temporary file of a file that the export replaces takes the mode
// of that file; a new one gets the mode that the umask gives.
func (s *staging) writeTemp(ctx context.Context, db Database, j *job) (err error) {
	perm := newFile
	if j.replace {
		perm = 0o600
	}
	f, err := createTemp(filepath.Dir(j.path), filepath.Base(j.path), perm)
	if err != nil {
		return fmt.Errorf("write %s: %w", j.path, err)
	}
	j.tmp = f.Name()
	s.temps = append(s.temps, j.tmp)
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("write %s: %w", j.path, closeErr)
		}
	}()
	if j.replace {
		if err := f.Chmod(j.mode); err != nil {
			return fmt.Errorf("write %s: %w", j.path, err)
		}
	}
	j.rows, err = db.Write(ctx, f, j.req)
	if err != nil {
		return fmt.Errorf("export table %s into %s: %w", j.req.Table, j.path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", j.path, err)
	}
	return nil
}

// createTemp creates a new file next to the file base in dir, with a name
// that starts with a dot, so that a plan never reads it.
func createTemp(dir, base string, perm fs.FileMode) (*os.File, error) {
	for range 10 {
		name := filepath.Join(dir, "."+base+"."+rand.Text()+".tmp")
		f, err := os.OpenFile(filepath.Clean(name), os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
	return nil, errors.New("no free name for a temporary file")
}

// move puts the temporary file of a job in place. The file that the
// declaration names is replaced by a rename. A new file is linked to its
// name, which fails when a file has appeared there since the checks: an
// export never replaces a file that the declaration does not name.
func (s *staging) move(j *job) error {
	if j.replace {
		if err := os.Rename(j.tmp, j.path); err != nil {
			return fmt.Errorf("write %s: %w", j.path, err)
		}
	} else {
		if err := os.Link(j.tmp, j.path); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("%s: a file appeared there while the export ran, and an export replaces only the file that declares a table: move the file, and export again", j.path)
			}
			return fmt.Errorf("write %s: %w", j.path, err)
		}
		// The rows are in place; the name that starts with a dot is left
		// only if it cannot be removed, and a plan never reads it.
		_ = os.Remove(j.tmp)
	}
	s.temps = slices.DeleteFunc(s.temps, func(t string) bool { return t == j.tmp })
	return nil
}

// discard removes the temporary files that have not moved into place, and
// the directories that the run created and left empty.
func (s *staging) discard() {
	for _, t := range s.temps {
		_ = os.Remove(t)
	}
	s.temps = nil
	for _, d := range slices.Backward(s.created) {
		// A directory that holds a file that moved into place stays.
		_ = os.Remove(d)
	}
}

// collisions returns an error for each pair of jobs whose files are one file
// on a file system that ignores case or the form of Unicode characters, and
// for each new file whose path is such a file of the directory already.
func collisions(dir string, jobs []*job) []error {
	var errs []error
	seen := make(map[string]*job, len(jobs))
	for _, j := range jobs {
		k := fold(j.rel)
		if o, ok := seen[k]; ok {
			errs = append(errs, fmt.Errorf("tables %s and %s would be written to %s and %s, which are one file on a file system that ignores case or the form of Unicode characters: "+
				"export them into two directories, or rename one of them", o.req.Table, j.req.Table, o.path, j.path))
			continue
		}
		seen[k] = j
		if !j.replace {
			if err := clash(dir, j); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}

// clash returns an error when the path of a new file names a file of the
// directory, or a part of it names an entry of the directory in another case
// or form. It compares the names that the directory lists, so the result is
// the same on every file system.
func clash(dir string, j *job) error {
	cur := dir
	parts := strings.Split(j.rel, "/")
	for i, part := range parts {
		entries, err := os.ReadDir(cur)
		if errors.Is(err, fs.ErrNotExist) {
			// Nothing of the path exists yet, so nothing can clash.
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the directory %s: %w", cur, err)
		}
		exact := false
		for _, e := range entries {
			if e.Name() == part {
				exact = true
				if i < len(parts)-1 && !e.IsDir() {
					return fmt.Errorf("%s is not a directory, and table %s would be written into it: move the file, or export into another directory",
						filepath.Join(cur, part), j.req.Table)
				}
				continue
			}
			if fold(e.Name()) == fold(part) {
				return fmt.Errorf("table %s would be written to %s, but %s exists, which is the same name on a file system that ignores case or the form of Unicode characters: "+
					"rename one of them, or export into another directory", j.req.Table, j.path, filepath.Join(cur, e.Name()))
			}
		}
		switch {
		case !exact:
			return nil
		case i == len(parts)-1:
			return fmt.Errorf("%s: the file exists, and it does not declare table %s: move the file, or name it in the export of the tables that it declares", j.path, j.req.Table)
		}
		cur = filepath.Join(cur, part)
	}
	return nil
}

// fold returns s as a file system that ignores case and the form of Unicode
// characters compares it.
func fold(s string) string {
	return cases.Fold().String(norm.NFC.String(s))
}
