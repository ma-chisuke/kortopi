package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLicenseFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"LICENSE":                         {Data: []byte("MIT")},
		"LICENSE.POSTGRESQL":              {Data: []byte("PostgreSQL")},
		"NOTICE.txt":                      {Data: []byte("notice")},
		"PATENTS":                         {Data: []byte("patents")},
		"README.md":                       {Data: []byte("readme")},
		"notice_response.go":              {Data: []byte("package x")},
		"pgproto3/notice_response.go":     {Data: []byte("package pgproto3")},
		"internal/cparser/COPYRIGHT":      {Data: []byte("PostgreSQL copyright")},
		"internal/cparser/licenses.json":  {Data: []byte("{}")},
		"testdata/LICENSE":                {Data: []byte("fixture")},
		".github/LICENSE":                 {Data: []byte("hidden")},
		"_examples/COPYING":               {Data: []byte("ignored by go")},
		"third_party/zlib/LICENSE-ZLIB":   {Data: []byte("zlib")},
		"third_party/zlib/license-header": {Data: []byte("zlib header")},
	}

	got, err := LicenseFiles(fsys, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"LICENSE", "LICENSE.POSTGRESQL", "NOTICE.txt", "PATENTS",
		"internal/cparser/COPYRIGHT", "third_party/zlib/LICENSE-ZLIB", "third_party/zlib/license-header",
	}
	if !slices.Equal(got, want) {
		t.Errorf("LicenseFiles(recursive) = %q, want %q", got, want)
	}

	got, err = LicenseFiles(fsys, false)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"LICENSE", "LICENSE.POSTGRESQL", "NOTICE.txt", "PATENTS"}
	if !slices.Equal(got, want) {
		t.Errorf("LicenseFiles(root) = %q, want %q", got, want)
	}
}

func TestHasLicense(t *testing.T) {
	for _, tc := range []struct {
		files []string
		want  bool
	}{
		{[]string{"LICENSE"}, true},
		{[]string{"license.md"}, true},
		{[]string{"LICEN" + "CE"}, true}, // the British spelling
		{[]string{"COPYING"}, true},
		{[]string{"UNLICENSE"}, true},
		{[]string{"LICENSE-APACHE", "LICENSE-MIT"}, true},
		{[]string{"NOTICE", "PATENTS"}, false},
		{[]string{"sub/LICENSE"}, false},
		{nil, false},
	} {
		if got := HasLicense(tc.files); got != tc.want {
			t.Errorf("HasLicense(%q) = %v, want %v", tc.files, got, tc.want)
		}
	}
}

// trivyReport is a report of `trivy fs --scanners license --format json`, cut
// down to the fields that ReadTrivy reads.
const trivyReport = `{"Results": [
{"Target": "go.mod", "Class": "lang-pkgs", "Type": "gomod"},
{"Target": "go.mod", "Class": "license", "Licenses": [
{"Severity": "LOW", "PkgName": "example.com/mit", "Name": "MIT"},
{"Severity": "LOW", "PkgName": "example.com/multi", "Name": "MIT"},
{"Severity": "LOW", "PkgName": "example.com/multi", "Name": "Apache-2.0"},
{"Severity": "LOW", "PkgName": "example.com/multi", "Name": "MIT"},
{"Severity": "CRITICAL", "PkgName": "example.com/gpl", "Name": "GPL-3.0"},
{"Severity": "MEDIUM", "PkgName": "example.com/lgpl", "Name": "LGPL-3.0"}
]}
]}`

func TestReadTrivy(t *testing.T) {
	got, err := ReadTrivy(strings.NewReader(trivyReport))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]License{
		"example.com/mit":   {{"MIT", "LOW"}},
		"example.com/multi": {{"Apache-2.0", "LOW"}, {"MIT", "LOW"}},
		"example.com/gpl":   {{"GPL-3.0", "CRITICAL"}},
		"example.com/lgpl":  {{"LGPL-3.0", "MEDIUM"}},
	}
	if len(got) != len(want) {
		t.Fatalf("ReadTrivy = %v, want %v", got, want)
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Errorf("ReadTrivy[%s] = %v, want %v", k, got[k], v)
		}
	}

	if _, err := ReadTrivy(strings.NewReader("not json")); err == nil {
		t.Error("ReadTrivy(not json) returned no error")
	}
}

func TestCheck(t *testing.T) {
	gomod := []byte("module example.com/main\n\ngo 1.27.4\n\nrequire (\n" +
		"\texample.com/mit v1.0.0\n" +
		"\texample.com/gpl v1.0.0\n" +
		"\texample.com/unknown v1.0.0\n" +
		"\texample.com/lgpl v1.0.0\n" +
		")\n\nrequire example.com/none v1.0.0\n")
	mods := []Module{
		{Path: "example.com/gpl", Version: "v1.0.0"},
		{Path: "example.com/lgpl", Version: "v1.0.0"},
		{Path: "example.com/mit", Version: "v1.0.0"},
		{Path: "example.com/none", Version: "v1.0.0", ReplacePath: "./third_party/none"},
		{Path: "example.com/unknown", Version: "v1.0.0"},
	}
	files := map[string][]string{
		"example.com/gpl":     {"COPYING"},
		"example.com/lgpl":    {"LICENSE"},
		"example.com/mit":     {"LICENSE"},
		"example.com/none":    {"NOTICE"},
		"example.com/unknown": {"LICENSE.txt"},
	}
	licenses, err := ReadTrivy(strings.NewReader(trivyReport))
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, f := range Check(mods, files, licenses, gomod) {
		got = append(got, f.String())
	}
	want := []string{
		"go.mod:7: [notices/forbidden-license] example.com/gpl v1.0.0 is compiled into kortopi under GPL-3.0, which trivy.yaml forbids. Fix: replace the dependency; ADR-0006 forbids this license in the binary. See ADR-0030.",
		"go.mod:12: [notices/no-license-file] example.com/none v1.0.0 => ./third_party/none is compiled into kortopi, and its module has no license file (LICENSE, COPYING, or UNLICENSE) at its root, so the release cannot carry its license. Fix: replace the dependency with one that carries a license, or ask its maintainers to add one. See ADR-0030.",
		"go.mod:8: [notices/unidentified-license] example.com/unknown v1.0.0 is compiled into kortopi, and Trivy did not identify the license in LICENSE.txt. Fix: read the license, and decide in a decision record whether kortopi may ship it (ADR-0006); until then, replace the dependency. See ADR-0030.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Check =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWriteNotices(t *testing.T) {
	goSection, err := ReadSection("Go 1.27.4 (the standard library and the runtime)", nil,
		fstest.MapFS{"LICENSE": {Data: []byte("Go license\r\n")}, "PATENTS": {Data: []byte("\nGo patents  \n\n")}},
		[]string{"LICENSE", "PATENTS"})
	if err != nil {
		t.Fatal(err)
	}
	mod, err := ReadSection("example.com/multi v1.0.0", []License{{"Apache-2.0", "LOW"}, {"MIT", "LOW"}},
		fstest.MapFS{"LICENSE": {Data: []byte("MIT text\n")}, "sub/NOTICE": {Data: []byte("notice text\n")}},
		[]string{"LICENSE", "sub/NOTICE"})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := WriteNotices(&b, "v1.2.3", []Section{goSection, mod}); err != nil {
		t.Fatal(err)
	}
	want := `Third-party software in kortopi v1.2.3

The kortopi binaries of this release contain the software listed below,
each under its own license. This file holds the license and notice files
of each, as its authors publish them. kortopi itself is distributed under
the MIT License, in the file LICENSE.

- Go 1.27.4 (the standard library and the runtime)
- example.com/multi v1.0.0 (Apache-2.0, MIT)

` + rule + `
Go 1.27.4 (the standard library and the runtime)
` + rule + `

--- LICENSE ---

Go license

--- PATENTS ---

Go patents

` + rule + `
example.com/multi v1.0.0
License: Apache-2.0, MIT
` + rule + `

--- LICENSE ---

MIT text

--- sub/NOTICE ---

notice text
`
	if got := b.String(); got != want {
		t.Errorf("WriteNotices =\n%s\nwant\n%s", got, want)
	}
}

func TestLocate(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "third_party", "local")
	cached := filepath.Join(root, "cache", "example.com", "cached@v1.0.0")
	replaced := filepath.Join(root, "cache", "example.com", "fork@v1.1.0")
	for _, dir := range []string{local, cached, replaced} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	mods := []Module{
		{Path: "example.com/cached", Version: "v1.0.0"},
		{Path: "example.com/local", Version: "v0.0.0", ReplacePath: "./third_party/local", ReplaceVersion: "(devel)"},
		{Path: "example.com/original", Version: "v1.0.0", ReplacePath: "example.com/fork", ReplaceVersion: "v1.1.0"},
	}
	var asked []string
	download := func(_ context.Context, dir string, refs []string) (map[string]string, error) {
		if dir != root {
			t.Errorf("download ran in %s, want %s", dir, root)
		}
		asked = refs
		return map[string]string{"example.com/cached@v1.0.0": cached, "example.com/fork@v1.1.0": replaced}, nil
	}
	if err := Locate(context.Background(), root, mods, download); err != nil {
		t.Fatal(err)
	}
	if want := []string{"example.com/cached@v1.0.0", "example.com/fork@v1.1.0"}; !slices.Equal(asked, want) {
		t.Errorf("download asked for %q, want %q", asked, want)
	}
	for i, want := range []string{cached, local, replaced} {
		if mods[i].Dir != want {
			t.Errorf("the directory of %s is %s, want %s", mods[i].Path, mods[i].Dir, want)
		}
	}

	failing := func(context.Context, string, []string) (map[string]string, error) {
		return nil, errors.New("offline")
	}
	if err := Locate(context.Background(), root, mods, failing); err == nil {
		t.Error("Locate returned no error when the download failed")
	}
	missing := func(context.Context, string, []string) (map[string]string, error) {
		return map[string]string{}, nil
	}
	if err := Locate(context.Background(), root, mods, missing); err == nil {
		t.Error("Locate returned no error when a module had no directory")
	}
}
