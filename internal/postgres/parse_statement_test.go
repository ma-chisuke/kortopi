package postgres

import (
	"errors"
	"fmt"
	"testing"

	pg "github.com/pganalyze/pg_query_go/v6"
)

func TestParseInsertsSkipsSettings(t *testing.T) {
	t.Parallel()
	for _, stmt := range []string{
		"SET standard_conforming_strings = on;",
		"SET standard_conforming_strings TO 'on';",
		"SET standard_conforming_strings = ON;",
		"SET standard_conforming_strings = true;",
		"SET standard_conforming_strings = 'TRUE';",
		"SET standard_conforming_strings = 't';",
		"SET standard_conforming_strings = yes;",
		"SET standard_conforming_strings = 'y';",
		"SET standard_conforming_strings = 1;",
		"SET standard_conforming_strings = '1';",
		"SET LOCAL standard_conforming_strings = on;",
		`SET "Standard_Conforming_Strings" = on;`,
		"SET client_encoding = 'UTF8';",
		"SET client_encoding TO utf8;",
		"SET client_encoding = 'UTF-8';",
		"SET client_encoding = 'utf_8';",
		"SET client_encoding = unicode;",
		"SET CLIENT_ENCODING = 'Unicode';",
		"SET NAMES 'UTF8';",
		"SET NAMES 'utf-8';",
		"SELECT pg_catalog.set_config('standard_conforming_strings', 'on', false);",
		"SELECT pg_catalog.set_config('client_encoding', 'UTF8', false);",
		"SELECT pg_catalog.set_config('search_path', '', false);",
		"SELECT pg_catalog.set_config('search_path', 'a' || 'b', false);",
		"SELECT pg_catalog.setval('s.q', 1, true);",
		"SET search_path = s;",
		"SET TIME ZONE 'UTC';",
		"SET TIME ZONE utc;",
		"SET TIME ZONE 'Etc/UTC';",
		"SET TIME ZONE 0;",
		"SET TIME ZONE 0.0;",
		"SET timezone = 'etc/utc';",
		"SET LOCAL TimeZone TO 'UTC';",
		"SET SESSION timezone = '0';",
		"SELECT pg_catalog.set_config('TimeZone', 'UTC', false);",
		"SET DateStyle = ISO, MDY;",
		"SET datestyle TO 'iso, mdy';",
		"SET DateStyle = 'MDY,ISO';",
		"SET DateStyle = mdy, 'ISO';",
		"SET DateStyle = 'ISO';",
		"SET LOCAL DateStyle = iso;",
		"SELECT pg_catalog.set_config('datestyle', 'ISO, MDY', false);",
		"SET IntervalStyle = postgres;",
		"SET intervalstyle TO 'POSTGRES';",
		"SET SESSION IntervalStyle = 'postgres';",
		"SELECT pg_catalog.set_config('IntervalStyle', 'postgres', false);",
	} {
		got, err := parseRun(t, stmt+"\nINSERT INTO s.t (a) VALUES (1);\n")
		if err != nil || len(got) != 1 {
			t.Errorf("%s: got %d statements and the error %v; want 1 statement", stmt, len(got), err)
		}
	}
}

func TestParseInsertsRejectsSettings(t *testing.T) {
	t.Parallel()
	const standard = "seed.sql:2: standard_conforming_strings must stay on, but this statement sets it %s; write the strings in the standard form, where a backslash is an ordinary character and E'...' holds escapes, and remove the statement"
	const encoding = "seed.sql:2: client_encoding must stay UTF8, but this statement sets it %s; convert the file to UTF-8 and remove the statement"
	const zone = "seed.sql:2: TimeZone must stay UTC, but this statement sets it %s; write the offset in each value, as in '2026-04-01 09:00:00+09', and remove the statement"
	const dates = "seed.sql:2: DateStyle must stay ISO, MDY, but this statement sets it %s; write dates as YYYY-MM-DD and remove the statement"
	const intervals = "seed.sql:2: IntervalStyle must stay postgres, but this statement sets it %s; write intervals in the form of the postgres style, such as '1 day 02:00:00', and remove the statement"
	tests := []struct {
		stmt string
		want string
		how  string
	}{
		{"SET standard_conforming_strings = off;", standard, "to off"},
		{"SET standard_conforming_strings TO 'false';", standard, "to 'false'"},
		{"SET standard_conforming_strings = 0;", standard, "to 0"},
		{"SET standard_conforming_strings = no;", standard, "to no"},
		{"SET standard_conforming_strings = o;", standard, "to o"},
		{"SET standard_conforming_strings = onx;", standard, "to onx"},
		{"SET standard_conforming_strings = '';", standard, "to ''"},
		{"SET standard_conforming_strings = 2;", standard, "to 2"},
		{"SET standard_conforming_strings = 1.0;", standard, "to 1.0"},
		{"SET SESSION standard_conforming_strings = off;", standard, "to off"},
		{"SET standard_conforming_strings TO DEFAULT;", standard, "to DEFAULT"},
		{"SET standard_conforming_strings FROM CURRENT;", standard, "FROM CURRENT"},
		{"SET standard_conforming_strings = on, off;", standard, "to on, off"},
		{"SELECT pg_catalog.set_config('standard_conforming_strings', 'off', false);", standard, "to 'off'"},
		{"SELECT pg_catalog.set_config('Standard_Conforming_Strings', 'off', true);", standard, "to 'off'"},
		{"SET client_encoding = 'LATIN1';", encoding, "to 'LATIN1'"},
		{"SET client_encoding TO sql_ascii;", encoding, "to sql_ascii"},
		{"SET client_encoding = 'UTF16';", encoding, "to 'UTF16'"},
		{"SET client_encoding = 6;", encoding, "to 6"},
		{"SET client_encoding TO DEFAULT;", encoding, "to DEFAULT"},
		{"SET NAMES 'LATIN1';", encoding, "to 'LATIN1'"},
		{"SET NAMES;", encoding, "to DEFAULT"},
		{"SELECT pg_catalog.set_config('client_encoding', 'LATIN1', false);", encoding, "to 'LATIN1'"},
		{"SELECT pg_catalog.set_config('client_encoding', NULL, false);", encoding, "to NULL"},
		{"SELECT pg_catalog.set_config('client_encoding', 'UT' || 'F8', false);", encoding, "to an expression"},
		{"SET TIME ZONE 'Asia/Tokyo';", zone, "to 'Asia/Tokyo'"},
		{"SET TIME ZONE 9;", zone, "to 9"},
		{"SET TIME ZONE -0.5;", zone, "to -0.5"},
		{"SET TIME ZONE LOCAL;", zone, "to DEFAULT"},
		{"SET TIME ZONE DEFAULT;", zone, "to DEFAULT"},
		{"SET TIME ZONE INTERVAL '+00:00' HOUR TO MINUTE;", zone, "to an expression"},
		{"SET timezone = 'GMT';", zone, "to 'GMT'"},
		{"SET LOCAL TimeZone TO 'UTC+1';", zone, "to 'UTC+1'"},
		{"SET SESSION timezone = 'utc ';", zone, "to 'utc '"},
		{"SELECT pg_catalog.set_config('timezone', 'America/New_York', false);", zone, "to 'America/New_York'"},
		{"SET DateStyle = ISO, DMY;", dates, "to ISO, DMY"},
		{"SET datestyle TO 'German';", dates, "to 'German'"},
		{"SET DateStyle = 'SQL, MDY';", dates, "to 'SQL, MDY'"},
		{"SET DateStyle = 'MDY';", dates, "to 'MDY'"},
		{"SET DateStyle = 'ISO,';", dates, "to 'ISO,'"},
		{"SET LOCAL DateStyle = 'ISO, YMD';", dates, "to 'ISO, YMD'"},
		{"SET DateStyle TO DEFAULT;", dates, "to DEFAULT"},
		{"SELECT pg_catalog.set_config('DateStyle', 'Postgres, DMY', false);", dates, "to 'Postgres, DMY'"},
		{"SET IntervalStyle = sql_standard;", intervals, "to sql_standard"},
		{"SET intervalstyle TO 'iso_8601';", intervals, "to 'iso_8601'"},
		{"SET SESSION IntervalStyle = postgres_verbose;", intervals, "to postgres_verbose"},
		{"SET IntervalStyle FROM CURRENT;", intervals, "FROM CURRENT"},
		{"SELECT pg_catalog.set_config('intervalstyle', 'sql_standard', true);", intervals, "to 'sql_standard'"},
	}
	for _, tt := range tests {
		// The statement comes after an INSERT, so the error names line 2.
		_, err := parseRun(t, "INSERT INTO s.t (a) VALUES (1);\n"+tt.stmt+"\nINSERT INTO s.t (a) VALUES (2);\n")
		want := fmt.Sprintf(tt.want, tt.how)
		if err == nil || err.Error() != want {
			t.Errorf("%s: got the error\n%v\nwant\n%s", tt.stmt, err, want)
		}
	}
}

func TestParseInsertsRejectsSettingName(t *testing.T) {
	t.Parallel()
	parseCheckErrors(t, []parseErrorCase{{
		name: "a name that is not a constant",
		src:  "SET a = 1;\nSELECT pg_catalog.set_config('standard_' || 'conforming_strings', 'off', false);\n",
		want: "seed.sql:2: the name of the setting in pg_catalog.set_config(...) is not a string constant; write the name as a constant, so that kortopi can tell that the statement does not change what the values mean",
	}})
}

func TestParseInsertsRecoversPanic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		panic any
		want  string
	}{
		{
			name:  "a string",
			panic: "out of memory",
			want:  "seed.sql: the SQL reader could not hold the file (panic: out of memory); declare large tables in CSV files, which kortopi loads with COPY without parsing them",
		},
		{
			name:  "an error",
			panic: errors.New("wasm error: unreachable"),
			want:  "seed.sql: the SQL reader could not hold the file (panic: wasm error: unreachable); declare large tables in CSV files, which kortopi loads with COPY without parsing them",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := Parser{parse: func(string) (*pg.ParseResult, error) { panic(tt.panic) }}
			got, err := p.ParseInserts(parseFile, []byte("INSERT INTO s.t (a) VALUES (1);\n"))
			if got != nil {
				t.Errorf("got the statements %+v, want none", got)
			}
			if err == nil || err.Error() != tt.want {
				t.Errorf("got the error\n%v\nwant\n%s", err, tt.want)
			}
		})
	}
}
