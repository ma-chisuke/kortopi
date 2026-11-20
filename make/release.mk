# Releases: the archives of the binaries, their checksums, the notices of the
# third-party software in them, and the GitHub release (ADR-0030). The
# targets run tools/release/release.sh; .github/workflows/release.yml runs the
# targets, and docs/guides/release.md tells the maintainer when to run them.
#
#   make release-build VERSION=v1.2.3     build the archives into dist/
#   make release-gate VERSION=v1.2.3      the tag is checked out, on main, and CI passed
#   make release-publish VERSION=v1.2.3   check dist/ and create the GitHub release
#   make release-verify VERSION=v1.2.3    download the release and verify it

.PHONY: release-build release-gate release-publish release-verify

CHECK_ALL_TARGETS += test-release

RELEASE := tools/release/release.sh
DIST ?= dist

# Fail before anything runs when VERSION is missing.
release-version = $(if $(VERSION),,$(error set VERSION, as in make $@ VERSION=v1.2.3))

release-build: ## Build the release archives into dist/ (VERSION=v1.2.3)
	$(release-version)
	@$(RELEASE) build "$(VERSION)" "$(DIST)"

release-gate:
	$(release-version)
	@$(RELEASE) gate "$(VERSION)"

release-publish:
	$(release-version)
	@$(RELEASE) publish "$(VERSION)" "$(DIST)"

release-verify: ## Download a published release and verify it (VERSION=v1.2.3)
	$(release-version)
	@$(RELEASE) verify "$(VERSION)"

# The release build of the commit, twice: both builds must give the same bytes,
# the notices must list the modules of the binaries, and every module must have
# a license that trivy.yaml allows. It builds for every platform, so it runs in
# check-all, when the code, the modules, or the release tooling is in scope.
RELEASE_GLOBS := "*.go" "go.mod" "go.sum" "tools/release/*" "LICENSE" "README.md" "README.ja.md" "trivy.yaml"

test-release:
	@$(TASK) $@ 'if [ -n "$$($(FILES) $(RELEASE_GLOBS))" ]; then $(RELEASE) test; fi'
