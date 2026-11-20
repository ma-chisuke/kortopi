// Package release builds the parts of a release of kortopi that the go
// command does not: the notices of the third-party software compiled into the
// binaries, archives whose bytes depend only on their contents, and the
// checksum file (ADR-0030).
package release

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Module is a module compiled into a binary, as `go version -m` lists it.
type Module struct {
	// Path and Version are the module as go.mod requires it.
	Path    string
	Version string
	// Replace is the replacement of the module: a module path and version, or
	// a directory, whose version is "(devel)" in the build information. Empty
	// when the module is not replaced.
	ReplacePath    string
	ReplaceVersion string
	// Dir holds the files of the module: a directory of the module cache, or
	// the directory of a local replacement. Locate fills it in.
	Dir string
}

// String names the module as `go version -m` does: its path and version.
func (m Module) String() string {
	return m.Path + " " + m.Version
}

// local reports whether the module is replaced by a directory.
func (m Module) local() bool {
	return m.ReplacePath != "" && (m.ReplaceVersion == "" || m.ReplaceVersion == "(devel)")
}

// source is the module@version whose files the module has: the replacement
// when there is one.
func (m Module) source() string {
	if m.ReplacePath != "" {
		return m.ReplacePath + "@" + m.ReplaceVersion
	}
	return m.Path + "@" + m.Version
}

// ReadBinaries returns the Go version that built the binaries and the modules
// compiled into any of them, sorted by path. Every binary must be built by the
// same Go version, and a module must have one version in all of them.
func ReadBinaries(paths []string) (string, []Module, error) {
	if len(paths) == 0 {
		return "", nil, errors.New("no binary given")
	}
	goVersion := ""
	byPath := map[string]Module{}
	for _, p := range paths {
		info, err := buildinfo.ReadFile(p)
		if err != nil {
			return "", nil, fmt.Errorf("read the build information of %s: %w", p, err)
		}
		if goVersion == "" {
			goVersion = info.GoVersion
		} else if info.GoVersion != goVersion {
			return "", nil, fmt.Errorf("%s was built by %s, and another binary by %s", p, info.GoVersion, goVersion)
		}
		for _, dep := range info.Deps {
			m := Module{Path: dep.Path, Version: dep.Version}
			if dep.Replace != nil {
				m.ReplacePath = dep.Replace.Path
				m.ReplaceVersion = dep.Replace.Version
			}
			if prev, ok := byPath[m.Path]; ok && prev != m {
				return "", nil, fmt.Errorf("%s holds %s, and another binary holds %s", p, describe(m), describe(prev))
			}
			byPath[m.Path] = m
		}
	}
	mods := make([]Module, 0, len(byPath))
	for _, m := range byPath {
		mods = append(mods, m)
	}
	slices.SortFunc(mods, func(a, b Module) int { return strings.Compare(a.Path, b.Path) })
	return goVersion, mods, nil
}

// describe names a module with its replacement.
func describe(m Module) string {
	if m.ReplacePath == "" {
		return m.String()
	}
	return m.String() + " => " + strings.TrimSpace(m.ReplacePath+" "+m.ReplaceVersion)
}

// Downloader returns the directory in the module cache of each module@version
// in refs, downloading the modules that are missing. The keys of the result
// are the refs.
type Downloader func(ctx context.Context, root string, refs []string) (map[string]string, error)

// GoModDownload is the Downloader that runs `go mod download -json` in root,
// the directory of the main module, so that go.sum verifies every download.
func GoModDownload(ctx context.Context, root string, refs []string) (map[string]string, error) {
	dirs := map[string]string{}
	if len(refs) == 0 {
		return dirs, nil
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "go", append([]string{"mod", "download", "-json"}, refs...)...) //nolint:gosec // runs the go command with module versions read from the binaries
	cmd.Dir = root
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	// The output holds one JSON object per module, with an Error for a module
	// that could not be downloaded, also when the command fails.
	dec := json.NewDecoder(&stdout)
	var problems []string
	for {
		var m struct {
			Path    string `json:"Path"`
			Version string `json:"Version"`
			Dir     string `json:"Dir"`
			Error   string `json:"Error"`
		}
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("read the output of go mod download: %w", err)
		}
		if m.Error != "" {
			problems = append(problems, m.Path+"@"+m.Version+": "+m.Error)
			continue
		}
		dirs[m.Path+"@"+m.Version] = m.Dir
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("go mod download: %s", strings.Join(problems, "; "))
	}
	if runErr != nil {
		return nil, fmt.Errorf("go mod download: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	return dirs, nil
}

// Locate sets the directory of each module: the directory of a local
// replacement, relative to root, or the directory in the module cache that
// download returns.
func Locate(ctx context.Context, root string, mods []Module, download Downloader) error {
	var refs []string
	for _, m := range mods {
		if !m.local() {
			refs = append(refs, m.source())
		}
	}
	dirs, err := download(ctx, root, refs)
	if err != nil {
		return err
	}
	for i, m := range mods {
		if m.local() {
			dir := m.ReplacePath
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(root, dir)
			}
			mods[i].Dir = dir
			continue
		}
		dir, ok := dirs[m.source()]
		if !ok {
			return fmt.Errorf("go mod download returned no directory for %s", m.source())
		}
		mods[i].Dir = dir
	}
	for _, m := range mods {
		if st, err := os.Stat(m.Dir); err != nil || !st.IsDir() {
			return fmt.Errorf("the files of %s are not in %s", describe(m), m.Dir)
		}
	}
	return nil
}
