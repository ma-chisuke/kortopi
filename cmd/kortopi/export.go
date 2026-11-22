package main

import (
	"context"
	"errors"
	"flag"
	"strings"

	"github.com/ma-chisuke/kortopi/internal/config"
	"github.com/ma-chisuke/kortopi/internal/declaration"
	"github.com/ma-chisuke/kortopi/internal/dialect"
	"github.com/ma-chisuke/kortopi/internal/export"
)

// exportOnlyFlags is the help of the flags that only export has.
const exportOnlyFlags = "  --schema <name>   export every table of this schema; repeat the flag for\n" +
	"                    more schemas\n" +
	"  --table <name>    export this table, written as <schema>.<table>; repeat\n" +
	"                    the flag for more tables\n" +
	"  --format <name>   csv (the default), or sql: the format of a new file\n" +
	"  --overwrite       replace the files that declare the exported tables\n"

// exportFlags lists the flags of export, for its help.
const exportFlags = "  --dir <path>      the data directory to write into, created if it is\n" +
	"                    missing (default: the current directory)\n" +
	exportOnlyFlags +
	lockTimeoutFlag +
	connectionFlags

// exportUsage is the short help of export, which follows an error in its
// flags.
const exportUsage = "usage: kortopi export [flags]\n" +
	"\n" +
	"flags:\n" +
	exportFlags

// exportHelp is the help of export.
const exportHelp = "usage: kortopi export [flags]\n" +
	"\n" +
	"Write the rows of tables of the database as a declaration: a file for each\n" +
	"table, which a plan of the same rows finds equal. An export only reads\n" +
	"the database.\n" +
	"\n" +
	"Name the tables with --table or --schema: an export never writes every\n" +
	"table of a database.\n" +
	"\n" +
	"A table that no file declares gets a new file, <schema>/<table>.csv, or\n" +
	".sql with --format sql. It holds every column of the table but the\n" +
	"generated ones and those that on_insert in kortopi.toml names: remove the\n" +
	"columns that the declaration should not manage.\n" +
	"\n" +
	"A table that a file declares already is written again only with\n" +
	"--overwrite, into that file, in its format, and with its columns.\n" +
	"\n" +
	"flags:\n" +
	exportFlags +
	"\n" +
	connectionHelp +
	"\n" +
	"exit status:\n" +
	"  0   the files were written\n" +
	"  1   the export failed; the error names the files that it wrote before\n" +
	"      it failed\n"

// exportOptions are the flags of export.
type exportOptions struct {
	common
	tables []declaration.TableName
	format declaration.Format
	// formatGiven is true when the user gave --format.
	formatGiven bool
	overwrite   bool
}

// errExportInterrupted is the first line of the error of an export that an
// interrupt stopped.
var errExportInterrupted = errors.New("interrupted: the export was stopped")

// runExport runs `kortopi export`.
func runExport(ctx context.Context, args []string, e env) int {
	o, err := parseExport(args)
	if err != nil {
		return flagError(e, err, exportHelp, exportUsage)
	}
	if err := makeExport(ctx, o, e); err != nil {
		// After an interrupt the error still says which files were written.
		if ctx.Err() != nil {
			printError(e.stderr, errExportInterrupted)
		}
		printError(e.stderr, err)
		return exitError
	}
	return exitOK
}

// parseExport parses the flags of export.
func parseExport(args []string) (exportOptions, error) {
	var o exportOptions
	if err := parse(exportFlagSet(&o), args, "give the data directory with --dir"); err != nil {
		return exportOptions{}, err
	}
	return o, nil
}

// exportFlagSet returns the flags of export, which parsing writes into o.
func exportFlagSet(o *exportOptions) *flag.FlagSet {
	fs := newFlagSet("export")
	o.define(fs)
	o.format = declaration.CSV
	fs.Func("table", "", func(s string) error {
		schema, name, ok := strings.Cut(s, ".")
		if !ok || schema == "" || name == "" || strings.Contains(name, ".") {
			return errors.New("write the table as <schema>.<table>, with one dot")
		}
		o.tables = append(o.tables, declaration.TableName{Schema: schema, Name: name})
		return nil
	})
	fs.Func("format", "", func(s string) error {
		switch s {
		case "csv":
			o.format = declaration.CSV
		case "sql":
			o.format = declaration.SQL
		default:
			return errors.New("give csv or sql")
		}
		o.formatGiven = true
		return nil
	})
	fs.BoolVar(&o.overwrite, "overwrite", false, "")
	return fs
}

// makeExport chooses the dialect, reads the settings and the declaration
// that the data directory holds already, if any, checks the flags against
// them, and only then connects and exports.
func makeExport(ctx context.Context, o exportOptions, e env) (err error) {
	d, err := e.dialects.Choose()
	if err != nil {
		return err
	}
	// The directory may be missing, and the export creates it, but a path to
	// a file is named as such before anything is read under it.
	if err := declaration.CheckDir(o.dir, true); err != nil {
		return err
	}
	settings, err := config.Load(o.dir)
	if err != nil {
		return err
	}
	decl, err := declaration.LoadExisting(o.dir, d.Parser())
	if err != nil {
		return err
	}
	opts := export.Options{Tables: o.tables, Schemas: o.schemas, Format: o.format, FormatGiven: o.formatGiven, Overwrite: o.overwrite, Notes: e.stderr}
	if err := export.Check(decl, opts); err != nil {
		return err
	}
	db, err := o.open(ctx, d, dialect.Options{Dir: decl.Dir, Export: true}, e)
	if err != nil {
		return err
	}
	defer func() {
		switch closeErr := closeSession(ctx, db); {
		case closeErr != nil && err == nil:
			err = closeErr
		case closeErr != nil:
			err = withNotEnded(err, closeErr)
		}
	}()
	_, err = export.Run(ctx, decl, settings, db, opts, e.stdout)
	return err
}
