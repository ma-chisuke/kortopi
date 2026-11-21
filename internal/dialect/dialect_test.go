package dialect

import (
	"context"
	"errors"
	"testing"

	"github.com/ma-chisuke/kortopi/internal/declaration"
)

// named is a dialect that has a name and nothing else.
type named string

func (n named) Name() string { return string(n) }

func (named) Parser() declaration.SQLParser { return nil }

func (named) Open(context.Context, Options) (Session, error) {
	return nil, errors.New("no database in this test")
}

func TestChoose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dialects []Dialect
		want     string
		wantErr  string
	}{
		{
			name:    "no dialect",
			wantErr: "this build of kortopi supports no database: build kortopi again without the build tags that leave a database out, kortopi_no_<database>",
		},
		{name: "one dialect", dialects: []Dialect{named("first")}, want: "first"},
		{
			name:     "several dialects",
			dialects: []Dialect{named("first"), named("second")},
			wantErr:  "this build of kortopi supports several databases, first, second, and cannot choose among them yet: build kortopi with one of them",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, err := NewRegistry(tt.dialects...)
			if err != nil {
				t.Fatalf("NewRegistry() = %v", err)
			}
			d, err := r.Choose()
			switch {
			case tt.wantErr != "":
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("Choose() = %v, %v, want the error %q", d, err, tt.wantErr)
				}
			case err != nil || d.Name() != tt.want:
				t.Errorf("Choose() = %v, %v, want %s", d, err, tt.want)
			}
		})
	}
}

// A build without a dialect gets ErrNoDialect, which a command can tell
// apart, from an empty registry or from the zero value.
func TestChooseNoDialect(t *testing.T) {
	t.Parallel()

	empty, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() = %v", err)
	}
	for _, r := range []*Registry{empty, {}} {
		if _, err := r.Choose(); !errors.Is(err, ErrNoDialect) {
			t.Errorf("Choose() of an empty registry = %v, want ErrNoDialect", err)
		}
	}
}

// Two dialects of one name are a mistake of the build, which the command
// reports when it starts.
func TestNewRegistryTwice(t *testing.T) {
	t.Parallel()

	r, err := NewRegistry(named("first"), named("second"), named("first"))
	want := "this build of kortopi holds the database first twice: build kortopi again with each database once"
	if r != nil || err == nil || err.Error() != want {
		t.Errorf("NewRegistry() of a name twice = %v, %v, want the error %q", r, err, want)
	}
}
