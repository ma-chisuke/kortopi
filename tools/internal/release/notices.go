package release

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/ma-chisuke/kortopi/tools/internal/finding"
)

// Rule identifiers.
const (
	RuleNoLicenseFile       = "notices/no-license-file"
	RuleUnidentifiedLicense = "notices/unidentified-license"
	RuleForbiddenLicense    = "notices/forbidden-license"
)

// see is the decision record that explains the notices.
const see = "ADR-0030"

// noticeName matches the base name of a file that holds a license or a
// notice: LICENSE (in the American or the British spelling), COPYING,
// COPYRIGHT, NOTICE, PATENTS, or UNLICENSE, in any case, alone or followed by
// a suffix such as ".txt", ".POSTGRESQL", or "-MIT".
var noticeName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|copyright|notice|patents|unlicense)([._-][a-z0-9._-]*)?$`)

// licenseName matches the base name of a file that grants a license, as
// opposed to a notice that only accompanies one.
var licenseName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|unlicense)([._-][a-z0-9._-]*)?$`)

// codeExtensions are the extensions of source files whose name happens to
// start like a notice, such as notice_response.go.
var codeExtensions = []string{
	".go", ".s", ".c", ".h", ".cc", ".cpp", ".rs", ".py", ".js", ".ts",
	".sh", ".json", ".yaml", ".yml", ".toml", ".proto", ".html", ".css",
}

// isNotice reports whether a file name is that of a license or a notice.
func isNotice(name string) bool {
	return noticeName.MatchString(name) && !slices.Contains(codeExtensions, strings.ToLower(path.Ext(name)))
}

// LicenseFiles returns the license and notice files in fsys, the files of a
// module, sorted with the files at the root first. With recursive false, it
// looks at the root only. Directories that Go ignores (testdata, and names
// that start with "." or "_") are skipped.
func LicenseFiles(fsys fs.FS, recursive bool) ([]string, error) {
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == "." {
				return nil
			}
			name := d.Name()
			if !recursive || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && isNotice(d.Name()) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(files, func(a, b string) int {
		ra, rb := !strings.Contains(a, "/"), !strings.Contains(b, "/")
		if ra != rb {
			if ra {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return files, nil
}

// HasLicense reports whether files, as LicenseFiles returns them, hold a
// license at the root of the module.
func HasLicense(files []string) bool {
	for _, f := range files {
		if !strings.Contains(f, "/") && licenseName.MatchString(f) {
			return true
		}
	}
	return false
}

// License is a license that Trivy identified for a module, with the severity
// that trivy.yaml gives it. A license that trivy.yaml forbids is CRITICAL.
type License struct {
	Name     string
	Severity string
}

// ReadTrivy reads the JSON report of `trivy fs --scanners license` and
// returns the licenses of each package, by package name. For a Go module, the
// package name is the module path.
func ReadTrivy(r io.Reader) (map[string][]License, error) {
	var report struct {
		Results []struct {
			Class    string `json:"Class"`
			Licenses []struct {
				PkgName  string `json:"PkgName"`
				Name     string `json:"Name"`
				Severity string `json:"Severity"`
			} `json:"Licenses"`
		} `json:"Results"`
	}
	if err := json.NewDecoder(r).Decode(&report); err != nil {
		return nil, fmt.Errorf("read the report of Trivy: %w", err)
	}
	licenses := map[string][]License{}
	for _, res := range report.Results {
		if res.Class != "license" {
			continue
		}
		for _, l := range res.Licenses {
			if l.PkgName == "" || l.Name == "" {
				continue
			}
			lic := License{Name: l.Name, Severity: l.Severity}
			if !slices.Contains(licenses[l.PkgName], lic) {
				licenses[l.PkgName] = append(licenses[l.PkgName], lic)
			}
		}
	}
	for _, ls := range licenses {
		slices.SortFunc(ls, func(a, b License) int { return strings.Compare(a.Name, b.Name) })
	}
	return licenses, nil
}

// Check returns a finding for each module that has no license file at its
// root, whose license Trivy did not identify, or whose license trivy.yaml
// forbids. files holds the license files of each module by path; gomod is the
// go.mod of the main module, which the findings point into.
func Check(mods []Module, files map[string][]string, licenses map[string][]License, gomod []byte) []finding.Finding {
	var findings []finding.Finding
	for _, m := range mods {
		f := finding.Finding{Path: "go.mod", Line: requireLine(gomod, m.Path), See: see}
		switch {
		case !HasLicense(files[m.Path]):
			f.Rule = RuleNoLicenseFile
			f.Message = fmt.Sprintf("%s is compiled into kortopi, and its module has no license file (LICENSE, COPYING, or UNLICENSE) at its root, so the release cannot carry its license", describe(m))
			f.Fix = "replace the dependency with one that carries a license, or ask its maintainers to add one"
		case len(licenses[m.Path]) == 0:
			f.Rule = RuleUnidentifiedLicense
			f.Message = fmt.Sprintf("%s is compiled into kortopi, and Trivy did not identify the license in %s", describe(m), strings.Join(files[m.Path], ", "))
			f.Fix = "read the license, and decide in a decision record whether kortopi may ship it (ADR-0006); until then, replace the dependency"
		default:
			var forbidden []string
			for _, l := range licenses[m.Path] {
				if l.Severity == "CRITICAL" {
					forbidden = append(forbidden, l.Name)
				}
			}
			if len(forbidden) == 0 {
				continue
			}
			f.Rule = RuleForbiddenLicense
			f.Message = fmt.Sprintf("%s is compiled into kortopi under %s, which trivy.yaml forbids", describe(m), strings.Join(forbidden, ", "))
			f.Fix = "replace the dependency; ADR-0006 forbids this license in the binary"
		}
		findings = append(findings, f)
	}
	return findings
}

// requireLine returns the line of go.mod that requires the module path, or 1.
func requireLine(gomod []byte, modPath string) int {
	sc := bufio.NewScanner(bytes.NewReader(gomod))
	for line := 1; sc.Scan(); line++ {
		fields := strings.Fields(sc.Text())
		if len(fields) > 0 && fields[0] == "require" {
			fields = fields[1:]
		}
		if len(fields) > 0 && fields[0] == modPath {
			return line
		}
	}
	return 1
}

// Section is the part of the notices for one component: the Go distribution
// or a module.
type Section struct {
	// Title names the component and its version.
	Title string
	// Licenses names the licenses that Trivy identified; empty for Go.
	Licenses []string
	// Files holds the license and notice files of the component.
	Files []NoticeFile
}

// NoticeFile is one license or notice file, with its path in the component.
type NoticeFile struct {
	Name string
	Text string
}

// ReadSection reads the license and notice files of a component from fsys.
func ReadSection(title string, licenses []License, fsys fs.FS, names []string) (Section, error) {
	s := Section{Title: title}
	for _, l := range licenses {
		s.Licenses = append(s.Licenses, l.Name)
	}
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return Section{}, err
		}
		s.Files = append(s.Files, NoticeFile{Name: name, Text: normalize(string(b))})
	}
	return s, nil
}

// normalize gives a text LF line breaks, no trailing spaces, and no blank
// lines at its start or its end, so that the notices do not depend on how the
// file was checked out.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

const rule = "================================================================================"

// WriteNotices writes the notices file of a release: an introduction, a list
// of the components, and the license and notice files of each.
func WriteNotices(w io.Writer, version string, sections []Section) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Third-party software in kortopi %s\n\n", version)
	b.WriteString("The kortopi binaries of this release contain the software listed below,\n" +
		"each under its own license. This file holds the license and notice files\n" +
		"of each, as its authors publish them. kortopi itself is distributed under\n" +
		"the MIT License, in the file LICENSE.\n\n")
	for _, s := range sections {
		fmt.Fprintf(&b, "- %s", s.Title)
		if len(s.Licenses) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(s.Licenses, ", "))
		}
		b.WriteString("\n")
	}
	for _, s := range sections {
		fmt.Fprintf(&b, "\n%s\n%s\n", rule, s.Title)
		if len(s.Licenses) > 0 {
			fmt.Fprintf(&b, "License: %s\n", strings.Join(s.Licenses, ", "))
		}
		b.WriteString(rule + "\n")
		for _, f := range s.Files {
			fmt.Fprintf(&b, "\n--- %s ---\n\n%s\n", f.Name, f.Text)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
