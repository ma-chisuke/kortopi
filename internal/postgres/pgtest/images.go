//go:build integration

package pgtest

// serverImages are the PostgreSQL servers of the integration tests: the
// official image of each major version that kortopi supports (ADR-0019),
// pinned by tag and digest. Renovate updates the tags and the digests
// (renovate.json). A new major version is added, and a retired one removed,
// by hand.
var serverImages = []string{
	"postgres:15.20@sha256:10f9e886f772e9d6484350b043bcf990ead67ae5d9988ddce265d72a299055d5",
	"postgres:16.16@sha256:3950ad7c3148ca2175ef1b6c36c9efbf78957ae8fbe8b3a5d7c35af50500382f",
	"postgres:17.12@sha256:b60e90042aa6b8dbecd58e224b3e36114dbf8ad754465264c2476ad58a86e45c",
	"postgres:18.7@sha256:e754b839f447639b7004895f4d8a4bfcb4e9dbe3a1bc035f27f646562a16fd61",
}
