package declaration_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

func TestOpenCSV(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "without byte order mark", content: "code,name\nx,y\n", want: "code,name\nx,y\n"},
		{name: "with byte order mark", content: "\xEF\xBB\xBFcode,name\r\nx,y\r\n", want: "code,name\r\nx,y\r\n"},
		{name: "byte order mark only", content: "\xEF\xBB\xBF", want: ""},
		{name: "byte order mark in a row", content: "code\n\xEF\xBB\xBF\n", want: "code\n\xEF\xBB\xBF\n"},
		{name: "shorter than a byte order mark", content: "a", want: "a"},
		{name: "start of a byte order mark", content: "\xEF\xBBx", want: "\xEF\xBBx"},
		{name: "empty", content: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := writeTree(t, map[string]string{"facility/rooms.csv": tt.content})
			r, err := declaration.OpenCSV(dir, csvTable("facility", "rooms", "code"))
			if err != nil {
				t.Fatalf("OpenCSV() error = %v", err)
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Errorf("Close() = %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("OpenCSV() reads %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenCSVLargeFile(t *testing.T) {
	t.Parallel()

	content := "code,name\n" + strings.Repeat("some code,some name\n", 10000)
	dir := writeTree(t, map[string]string{"facility/rooms.csv": "\xEF\xBB\xBF" + content})
	r, err := declaration.OpenCSV(dir, csvTable("facility", "rooms", "code"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("OpenCSV() reads %d bytes, want %d", len(got), len(content))
	}
}

func TestOpenCSVErrors(t *testing.T) {
	t.Parallel()

	dir := writeTree(t, map[string]string{"dump.sql": "", "facility/folder.csv/rows.csv": ""})
	tests := []struct {
		name  string
		table declaration.Table
		want  string
	}{
		{
			name:  "missing file",
			table: csvTable("facility", "rooms", "code"),
			want:  filepath.Join(dir, "facility", "rooms.csv") + ": no such file or directory",
		},
		{
			name:  "directory",
			table: csvTable("facility", "folder", "code"),
			want:  filepath.Join(dir, "facility", "folder.csv") + ": is a directory",
		},
		{
			name:  "SQL table",
			table: declaration.Table{Name: tableName("facility", "rooms"), Format: declaration.SQL, File: "dump.sql"},
			want:  "table facility.rooms is declared by dump.sql, not by a CSV file",
		},
		{
			name:  "path outside the directory",
			table: declaration.Table{Name: tableName("facility", "rooms"), Format: declaration.CSV, File: "../rooms.csv"},
			want:  filepath.Join(filepath.Dir(dir), "rooms.csv") + ": invalid argument",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, err := declaration.OpenCSV(dir, tt.table)
			if err == nil {
				_ = r.Close()
				t.Fatal("OpenCSV() error = nil, want an error")
			}
			if err.Error() != tt.want {
				t.Errorf("OpenCSV() error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestOpenCSVCurrentDirectory(t *testing.T) {
	// Not parallel: it changes the working directory.
	t.Chdir(writeTree(t, map[string]string{"facility/rooms.csv": "code\n"}))

	r, err := declaration.OpenCSV("", csvTable("facility", "rooms", "code"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if got, err := io.ReadAll(r); err != nil || string(got) != "code\n" {
		t.Errorf("OpenCSV(\"\") reads %q, %v, want %q", got, err, "code\n")
	}
}
