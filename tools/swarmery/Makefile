.PHONY: build build-web build-go copy-docs dev test clean install

# Repo docs snapshotted into the go:embed content dir for /api/docs.
DOCS_SRC := ../../docs
DOCS_DST := internal/docsfs/content

# Where the launchd service (`swarmery install`) expects the daemon binary,
# and the label it registers under.
INSTALL_BIN   := $(HOME)/.swarmery/bin/swarmery
LAUNCHD_LABEL := com.swarmery.daemon

# build: docs snapshot + vite bundle → go:embed → single binary ./swarmery
build: copy-docs build-web build-go

build-web:
	cd web && npm install && npm run build

build-go:
	go build -o swarmery ./cmd/swarmery

# install: rebuild, then replace the launchd-managed binary and restart the
# service. The binary is swapped via atomic rename (mv) so it never overwrites
# the running executable in place ("Text file busy" / ETXTBSY). If the launchd
# service isn't loaded yet, install the binary and point to `swarmery install`.
install: build
	mkdir -p $(dir $(INSTALL_BIN))
	cp -f swarmery $(INSTALL_BIN).new
	mv -f $(INSTALL_BIN).new $(INSTALL_BIN)
	@if launchctl print gui/$$(id -u)/$(LAUNCHD_LABEL) >/dev/null 2>&1; then \
		launchctl kickstart -k gui/$$(id -u)/$(LAUNCHD_LABEL) && \
		echo "→ restarted $(LAUNCHD_LABEL) on the new binary"; \
	else \
		echo "→ installed $(INSTALL_BIN); service not loaded — run '$(INSTALL_BIN) install' to set up launchd auto-start"; \
	fi

# copy-docs: snapshot the repo docs into internal/docsfs/content for go:embed.
# The copies are gitignored (.gitkeep keeps the embed pattern valid without
# them, e.g. in CI — /api/docs then serves an empty list).
copy-docs:
	cp $(DOCS_SRC)/ONBOARDING.md $(DOCS_SRC)/WORKFLOW.md $(DOCS_SRC)/EXTENDING.md $(DOCS_SRC)/NEUTRALITY.md $(DOCS_DST)/

# dev: run the API daemon and the vite dev server (proxying /api to :7777)
dev: copy-docs
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
