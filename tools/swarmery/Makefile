.PHONY: build build-web build-go copy-docs copy-probe dev test clean install

# Repo docs snapshotted into the go:embed content dir for /api/docs.
DOCS_SRC := ../../docs
DOCS_DST := internal/docsfs/content

# Build identity stamped into the binary so every rebuild reports the commit it
# came from instead of the frozen release semver (internal/version). Empty
# outside a git checkout — version.String() then falls back to the VCS revision
# Go stamps into the build info, and to bare semver if even that is missing.
VERSION_PKG := github.com/atretyak1985/swarmery/tools/swarmery/internal/version
BUILD_ID    := $(shell git describe --tags --match 'swarmery-v*' --always --dirty 2>/dev/null)

# Where the OS service manager expects the daemon binary, and the unit it
# runs under. macOS uses `swarmery install`'s launchd plist; Linux here runs
# it as a systemd --user unit pointed at ~/.local/bin/swarmery instead.
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Linux)
INSTALL_BIN    := $(HOME)/.local/bin/swarmery
SYSTEMD_UNIT   := swarmery.service
else
INSTALL_BIN   := $(HOME)/.swarmery/bin/swarmery
LAUNCHD_LABEL := com.swarmery.daemon
endif

# build: docs snapshot + vite bundle → go:embed → single binary ./swarmery
build: copy-docs copy-probe build-web build-go

# dist is pruned first: vite keeps emptyOutDir=false (web/dist/.gitkeep must
# survive for go:embed on fresh clones), so without this every build's hashed
# bundles accumulate and go:embed packs ALL of them — a long-lived checkout
# reached 211MB of stale assets and produced a 243MB daemon binary.
#
# npm ci, not npm install: the same install CI runs (swarmery-ci.yml), and it
# never rewrites package-lock.json. npm install let the local npm re-serialise
# the lockfile — an older npm drops the "libc" fields a newer one records — so
# every `make install` left a dirty lockfile in the checkout. A package.json the
# lockfile no longer matches now fails the build instead of being papered over.
build-web:
	cd web && rm -rf dist/assets dist/index.html && npm ci && npm run build

build-go:
	go build -ldflags "-X $(VERSION_PKG).Build=$(BUILD_ID)" -o swarmery ./cmd/swarmery

# install: rebuild, then replace the service-managed binary and restart it.
# The binary is swapped via atomic rename (mv) so it never overwrites the
# running executable in place ("Text file busy" / ETXTBSY). If no service is
# loaded yet, install the binary and point to the right setup command.
install: build
	mkdir -p $(dir $(INSTALL_BIN))
	cp -f swarmery $(INSTALL_BIN).new
	mv -f $(INSTALL_BIN).new $(INSTALL_BIN)
ifeq ($(UNAME_S),Linux)
	@if systemctl --user is-enabled $(SYSTEMD_UNIT) >/dev/null 2>&1; then \
		systemctl --user restart $(SYSTEMD_UNIT) && \
		echo "→ restarted $(SYSTEMD_UNIT) on the new binary"; \
	else \
		echo "→ installed $(INSTALL_BIN); no systemd --user unit found — see tools/swarmery/README.md to set one up"; \
	fi
else
	@if launchctl print gui/$$(id -u)/$(LAUNCHD_LABEL) >/dev/null 2>&1; then \
		launchctl kickstart -k gui/$$(id -u)/$(LAUNCHD_LABEL) && \
		echo "→ restarted $(LAUNCHD_LABEL) on the new binary"; \
	else \
		echo "→ installed $(INSTALL_BIN); service not loaded — run '$(INSTALL_BIN) install' to set up launchd auto-start"; \
	fi
endif

# copy-docs: snapshot the repo docs into internal/docsfs/content for go:embed.
# The copies are gitignored (.gitkeep keeps the embed pattern valid without
# them, e.g. in CI — /api/docs then serves an empty list).
#
# Guides are FLATTENED into the same embed root rather than kept in a subdir:
# readDocs (internal/api/docs.go) walks one level and skips directories, so a
# nested copy would simply never be served. The `guide-` filename prefix is
# what survives flattening, and it is also the dashboard's grouping key — the
# rail splits Guides from Reference on it.
#
# The guides are copied by `find`, not by a `guide-*.md` glob appended to the
# cp above: make's shell has no nullglob, so an empty guides dir would hand cp
# the literal pattern, cp would fail, and `make build` / `make dev` would break
# with it. find matching nothing is simply a no-op — and adding a guide still
# needs no Makefile edit.
copy-docs:
	cp $(DOCS_SRC)/ONBOARDING.md $(DOCS_SRC)/WORKFLOW.md $(DOCS_SRC)/EXTENDING.md $(DOCS_SRC)/NEUTRALITY.md $(DOCS_SRC)/PLUGINS.md docs/concepts.md docs/retro.md docs/usage.md docs/api-project-config.md $(DOCS_DST)/
	find docs/guides -maxdepth 1 -name 'guide-*.md' -exec cp {} $(DOCS_DST)/ \;

# copy-probe: snapshot the channel-probe harness (script + fixture plugin) into
# internal/channelprobe/script/ for go:embed, so `swarmery account doctor
# --probe` and the daemon's version-change ticker run it from the installed
# binary, which has no path to the repo. The copies are gitignored; the
# committed .gitkeep keeps `//go:embed all:script` valid without them (CI).
PROBE_DST := internal/channelprobe/script
copy-probe:
	rm -rf $(PROBE_DST)/cc-channel-probe.sh $(PROBE_DST)/fixtures
	mkdir -p $(PROBE_DST)/fixtures
	cp ../../scripts/tests/cc-channel-probe.sh $(PROBE_DST)/
	cp -R ../../scripts/tests/fixtures/cc-channel-probe $(PROBE_DST)/fixtures/

# dev: run the API daemon and the vite dev server (proxying /api to :7777).
# The proxy forwards the dev origin
# (http://localhost:5173) as Origin and Host, which the daemon's D4 fences only
# accept once opted in (internal/api/origin.go) — so it is, on top of whatever
# SWARMERY_TRUSTED_ORIGINS already holds in the calling shell.
dev: copy-docs copy-probe
	SWARMERY_TRUSTED_ORIGINS="$${SWARMERY_TRUSTED_ORIGINS:+$${SWARMERY_TRUSTED_ORIGINS},}http://localhost:5173,http://127.0.0.1:5173" \
		go run ./cmd/swarmery serve & \
	cd web && npm run dev; \
	kill %1 2>/dev/null

test:
	go vet ./...
	go test ./...

clean:
	rm -f swarmery
	find web/dist -mindepth 1 ! -name .gitkeep -delete
	find $(DOCS_DST) -mindepth 1 ! -name .gitkeep -delete
	rm -rf $(PROBE_DST)/cc-channel-probe.sh $(PROBE_DST)/fixtures
