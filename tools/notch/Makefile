# Build/test/run, plus packaging + install ergonomics that mirror
# tools/swarmery/Makefile (copy-then-rename, `launchctl kickstart -k`) so both
# companions feel the same to an operator.

.PHONY: build test run bundle install uninstall restart clean

BIN_NAME  := SwarmeryNotch
APP_NAME  := Swarmery Notch.app

# `make bundle` assembles the .app here -- a build artifact, not source
# (tools/notch/.gitignore excludes it, same as `.build/`).
DIST_DIR   := dist
APP_BUNDLE := $(DIST_DIR)/$(APP_NAME)

# Where `make install` places the app and its LaunchAgent, and the label it
# registers under.
INSTALL_APP   := $(HOME)/Applications/$(APP_NAME)
LAUNCHD_LABEL := com.swarmery.notch
LAUNCHD_PLIST := $(HOME)/Library/LaunchAgents/$(LAUNCHD_LABEL).plist
LOG_DIR       := $(HOME)/.swarmery/logs

build:
	swift build -c release

test:
	swift test

run:
	swift run SwarmeryNotch

# bundle: build a release binary and assemble a minimal but well-formed .app
# (Contents/MacOS + Contents/Info.plist). No code signing yet (ship unsigned
# first is a deliberate decision, not an oversight -- see README.md's
# Gatekeeper note); first launch needs a right-click -> Open override.
bundle: build
	rm -rf "$(APP_BUNDLE)"
	mkdir -p "$(APP_BUNDLE)/Contents/MacOS"
	cp .build/release/$(BIN_NAME) "$(APP_BUNDLE)/Contents/MacOS/$(BIN_NAME)"
	cp Resources/Info.plist "$(APP_BUNDLE)/Contents/Info.plist"
	@echo "→ built $(APP_BUNDLE)"

# install: bundle, then copy-then-rename into ~/Applications (same atomic-swap
# shape as tools/swarmery/Makefile's install target -- build the replacement
# fully before anything is removed, so a failed cp never leaves ~/Applications
# without an app). Writes the LaunchAgent plist (substituting the installed
# app's real binary path and the log dir) and either kickstarts an
# already-loaded service or bootstraps it fresh.
# The running agent is stopped BEFORE the bundle on disk is replaced: an
# AppKit process whose .app vanishes underneath it can trap inside layout
# (SIGTRAP in NSHostingView) instead of exiting cleanly, which is how the
# previous build died on every upgrade. bootout → swap → bootstrap gives a
# clean stop, a clean start on the new build, and one PID afterwards.
install: bundle
	mkdir -p "$(HOME)/Applications" "$(HOME)/Library/LaunchAgents" "$(LOG_DIR)"
	@if launchctl print gui/$$(id -u)/$(LAUNCHD_LABEL) >/dev/null 2>&1; then \
		launchctl bootout gui/$$(id -u)/$(LAUNCHD_LABEL) 2>/dev/null || true; \
		echo "→ stopped running $(LAUNCHD_LABEL) before replacing the bundle"; \
	fi
	rm -rf "$(INSTALL_APP).new"
	cp -R "$(APP_BUNDLE)" "$(INSTALL_APP).new"
	rm -rf "$(INSTALL_APP)"
	mv "$(INSTALL_APP).new" "$(INSTALL_APP)"
	sed -e "s|__APP_PATH__|$(INSTALL_APP)/Contents/MacOS/$(BIN_NAME)|" \
		-e "s|__LOG_DIR__|$(LOG_DIR)|" \
		Resources/$(LAUNCHD_LABEL).plist > "$(LAUNCHD_PLIST)"
	launchctl bootstrap gui/$$(id -u) "$(LAUNCHD_PLIST)"
	@echo "→ installed $(INSTALL_APP); loaded $(LAUNCHD_LABEL) on the new build"

# uninstall: unload the LaunchAgent and remove both it and the app bundle.
# Logs are left in place, matching the daemon's uninstall (rollback, not a
# purge).
uninstall:
	-launchctl bootout gui/$$(id -u)/$(LAUNCHD_LABEL) >/dev/null 2>&1
	rm -f "$(LAUNCHD_PLIST)"
	rm -rf "$(INSTALL_APP)"
	@echo "→ removed $(INSTALL_APP) and $(LAUNCHD_LABEL) (logs kept in $(LOG_DIR))"

# restart: kick the already-installed LaunchAgent without rebuilding -- the
# quick "it looks stuck, force a fresh process" operator action. Use `install`
# instead to pick up a new build.
restart:
	launchctl kickstart -k gui/$$(id -u)/$(LAUNCHD_LABEL)

clean:
	rm -rf $(DIST_DIR)
