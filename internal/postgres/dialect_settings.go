//go:build integration

package postgres

import (
	"maps"
	"slices"
	"strings"
)

// WithSettings returns the dialect whose sessions take settings, by the
// keywords of libpq, such as password, sslmode, connect_timeout, and
// options, as they take the environment variables of libpq, such as
// PGPASSWORD and PGOPTIONS, and in their place: the settings override the
// environment, and the flags of the connection override the settings. An
// empty value counts as unset, as an empty variable does.
//
// Only the integration tests build it. A test of the command gives the
// command through it what a user gives in the environment, and leaves the
// environment of the process alone: the tests that run in parallel share it
// (ADR-0052).
func WithSettings(settings map[string]string) Dialect {
	var parts []string
	for _, keyword := range slices.Sorted(maps.Keys(settings)) {
		if value := settings[keyword]; value != "" {
			parts = append(parts, keywordValue(keyword, value))
		}
	}
	return Dialect{settings: strings.Join(parts, " ")}
}
