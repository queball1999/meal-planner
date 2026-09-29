BINARY  = bin/goeat
GOWORK ?= off

.PHONY: all deps check build run clean \
        desktop-deps desktop-dev desktop-icons desktop-windows desktop-linux \
        docker-up docker-rebuild docker-build docker-restart docker-down docker-reset docker-logs docker-ps \
        docker-backup docker-restore trim-backup help

all: check build

## deps: tidy go modules
deps:
	GOWORK=$(GOWORK) go mod tidy

## check: fmt + vet + test (definition of done for each phase)
check: deps
	@echo "==> gofmt"
	@test -z "$$(GOWORK=$(GOWORK) gofmt -l .)" || \
		(echo "ERROR: files need formatting. Run: gofmt -w ." && exit 1)
	@echo "==> go vet"
	GOWORK=$(GOWORK) go vet ./...
	@echo "==> go test"
	GOWORK=$(GOWORK) go test ./...
	@echo "==> OK"

## build: compile to bin/goeat
build:
	@mkdir -p bin
	GOWORK=$(GOWORK) go build -o $(BINARY) .
	@echo "==> built $(BINARY)"

## run: run in development mode (auto-loads .env)
run:
	GOWORK=$(GOWORK) go run .

# --- Docker Compose ---
#
# Data (SQLite db + recipe images) lives in the named volume `goeat-data`, not
# a host bind mount: that is what makes `docker compose down -v` an actual
# reset. A bind mount can never be removed by `-v` (it only ever touches named
# volumes), so as long as the compose file names a volume here, the literal
# `docker compose down -v` command - typed by hand or via `make docker-down`
# - wipes the database and recipe images and puts the app back at the
# first-run setup wizard. There is no separate rm -rf step needed.

## docker-up: start the stack (no rebuild)
docker-up:
	docker compose up -d

## docker-rebuild: rebuild the image and restart the container
docker-rebuild:
	docker compose up -d --build

## docker-build: rebuild the image without (re)starting the container
docker-build:
	docker compose build

## docker-restart: restart the container
docker-restart:
	docker compose restart

## docker-down: stop and remove the stack (keeps the goeat-data volume)
docker-down:
	docker compose down

## docker-reset: stop the stack AND delete the goeat-data volume (resets db + setup wizard)
docker-reset:
	docker compose down -v

## docker-logs: follow container logs
docker-logs:
	docker compose logs -f

## docker-ps: list containers
docker-ps:
	docker compose ps

# --- Backup and restore ---
#
# Since the database file and recipe images live in a named Docker volume
# rather than a host directory, there's no `cp` for grabbing a copy - these
# targets tar the volume out to (and back in from) a zip on the host, the same
# approach docker-backup/docker-restore use for named volumes elsewhere.
# MSYS_NO_PATHCONV=1 is a Windows/Git-Bash-only fix: MSYS rewrites a leading
# "/backup" or "/volume" argument into a Windows path before Docker ever sees
# it, which breaks the in-container mount path. It's a no-op everywhere else.
BACKUP_DIR ?= backups
BACKUP_KEEP ?= 3

## docker-backup: archive the goeat-data volume + .env to $(BACKUP_DIR), then trim-backup
docker-backup:
	@mkdir -p $(BACKUP_DIR)
	@stamp=$$(date +%Y%m%d-%H%M%S); \
	work="$(BACKUP_DIR)/goeat-backup-$$stamp"; \
	mkdir -p "$$work"; \
	echo "Archiving goeat-data volume..."; \
	MSYS_NO_PATHCONV=1 docker run --rm -v goeat-data:/volume -v "$$(pwd)/$$work":/backup alpine tar czf /backup/goeat-data.tar.gz -C /volume .; \
	[ -f .env ] && cp .env "$$work/.env"; \
	echo "Zipping..."; \
	(cd $(BACKUP_DIR) && zip -rq "goeat-backup-$$stamp.zip" "goeat-backup-$$stamp"); \
	rm -rf "$$work"; \
	echo "Backup written to $(BACKUP_DIR)/goeat-backup-$$stamp.zip"
	@$(MAKE) trim-backup BACKUP_KEEP=$(BACKUP_KEEP)

## docker-restore: restore a docker-backup zip (BACKUP_FILE=...) into goeat-data
docker-restore:
	@if [ -z "$(BACKUP_FILE)" ]; then \
		echo "Usage: make docker-restore BACKUP_FILE=$(BACKUP_DIR)/goeat-backup-<timestamp>.zip"; \
		exit 1; \
	fi
	@if [ ! -f "$(BACKUP_FILE)" ]; then \
		echo "No such file: $(BACKUP_FILE)"; \
		exit 1; \
	fi
	@printf 'This overwrites .env and replaces the goeat-data volume with the contents of %s. Continue? [y/N] ' "$(BACKUP_FILE)"; \
	read confirm; \
	case "$$confirm" in y|Y|yes|YES) ;; *) echo "Aborted."; exit 1;; esac; \
	extract=$$(mktemp -d); \
	unzip -q "$(BACKUP_FILE)" -d "$$extract"; \
	src=$$(find "$$extract" -mindepth 1 -maxdepth 1 -type d); \
	if [ -z "$$src" ]; then echo "Archive did not contain the expected backup folder"; rm -rf "$$extract"; exit 1; fi; \
	[ -f "$$src/.env" ] && cp "$$src/.env" .env && echo "Restored .env"; \
	docker compose down; \
	echo "Restoring goeat-data volume..."; \
	MSYS_NO_PATHCONV=1 docker run --rm -v goeat-data:/volume -v "$$src":/backup alpine sh -c 'rm -rf /volume/..?* /volume/.[!.]* /volume/*; tar xzf /backup/goeat-data.tar.gz -C /volume'; \
	rm -rf "$$extract"; \
	echo "Restore complete. Start the stack: make docker-up"

## trim-backup: delete all but the BACKUP_KEEP most recent backups (default 3)
trim-backup:
	@count=$$(ls -1 $(BACKUP_DIR)/goeat-backup-*.zip 2>/dev/null | wc -l); \
	if [ "$$count" -le "$(BACKUP_KEEP)" ]; then \
		echo "$$count backup(s) in $(BACKUP_DIR), keeping up to $(BACKUP_KEEP) - nothing to trim."; \
	else \
		remove=$$((count - $(BACKUP_KEEP))); \
		ls -1 $(BACKUP_DIR)/goeat-backup-*.zip | sort | head -n "$$remove" | while IFS= read -r f; do \
			echo "Removing old backup: $$f"; \
			rm -f "$$f"; \
		done; \
		echo "Trimmed $(BACKUP_DIR) to the $(BACKUP_KEEP) most recent backups."; \
	fi

# --- Desktop app (Tauri, desktop/) ---
#
# The desktop app is the unchanged Go server run as a sidecar by a small Tauri
# shell (spec/commit-plan-phase14-09-28-26.md). Each OS target builds on that
# OS - Tauri doesn't cross-compile installers - and writes both flavours to
# output/<os>/:
#
#   output/windows/GoEat-<ver>-windows-x64-setup.exe     installer (NSIS, per-user)
#   output/windows/GoEat-<ver>-windows-x64-portable.zip  portable (data/ beside the exe)
#   output/linux/GoEat-<ver>-linux-amd64.deb             installer
#   output/linux/GoEat-<ver>-linux-amd64.AppImage        portable
#
# Needs Go, Node and Rust - on Windows also the MSVC C++ build tools, or run
# with RUSTUP_TOOLCHAIN=stable-x86_64-pc-windows-gnu, which brings its own
# linker. DESKTOP_VERSION defaults to tauri.conf.json's; CI passes the tag's.
DESKTOP_DIR     := desktop
TAURI_DIR       := $(DESKTOP_DIR)/src-tauri
DESKTOP_VERSION ?= $(shell node -p "require('./$(TAURI_DIR)/tauri.conf.json').version")
DESKTOP_RELEASE := $(TAURI_DIR)/target/release
DESKTOP_NAME     = GoEat-$(DESKTOP_VERSION)
OUT_WINDOWS     := output/windows
OUT_LINUX       := output/linux

ifdef RUSTUP_TOOLCHAIN
export RUSTUP_TOOLCHAIN
endif

# tauri build runs cmd/build_sidecar first (beforeBuildCommand), which stamps
# GOEAT_VERSION into the Go binary.
export GOEAT_VERSION = v$(DESKTOP_VERSION)
TAURI_BUILD = cd $(DESKTOP_DIR) && npx tauri build --config '{"version":"$(DESKTOP_VERSION)"}'

## desktop-deps: install the Tauri CLI into desktop/node_modules
desktop-deps:
	cd $(DESKTOP_DIR) && npm ci --no-audit --no-fund

## desktop-dev: run the desktop app from source
desktop-dev: desktop-deps
	cd $(DESKTOP_DIR) && npx tauri dev

## desktop-icons: regenerate the Windows/Linux icons from desktop/icon.svg
desktop-icons: desktop-deps
	cd $(DESKTOP_DIR) && npx tauri icon icon.svg --output src-tauri/icons
	rm -rf $(TAURI_DIR)/icons/android $(TAURI_DIR)/icons/ios $(TAURI_DIR)/icons/icon.icns \
		$(TAURI_DIR)/icons/Square*.png $(TAURI_DIR)/icons/StoreLogo.png

## desktop-windows: installer + portable zip into output/windows (run on Windows)
desktop-windows: desktop-deps
	$(TAURI_BUILD) --bundles nsis
	@mkdir -p $(OUT_WINDOWS)
	@for f in "$(DESKTOP_RELEASE)"/bundle/nsis/*_$(DESKTOP_VERSION)_*-setup.exe; do \
		cp "$$f" "$(OUT_WINDOWS)/$(DESKTOP_NAME)-windows-x64-setup.exe"; \
	done
	@# The shell is "Go Eat.exe", not "GoEat.exe": Windows names are
	@# case-insensitive, and the Go sidecar must be called goeat.exe.
	@# WebView2Loader.dll only exists for GNU-toolchain builds (MSVC links it
	@# statically); without it the shell exits silently at startup.
	@stage="$(OUT_WINDOWS)/$(DESKTOP_NAME)-windows-x64-portable"; \
	rm -rf "$$stage" "$$stage.zip" && mkdir -p "$$stage" && \
	cp "$(DESKTOP_RELEASE)/goeat-desktop.exe" "$$stage/Go Eat.exe" && \
	cp "$(DESKTOP_RELEASE)/goeat.exe" "$$stage/goeat.exe" && \
	cp "$(DESKTOP_DIR)/portable.txt" "$$stage/portable.txt" && \
	if [ -f "$(DESKTOP_RELEASE)/WebView2Loader.dll" ]; then \
		cp "$(DESKTOP_RELEASE)/WebView2Loader.dll" "$$stage/"; \
	fi && \
	powershell -NoProfile -Command "Compress-Archive -Path '$$stage/*' -DestinationPath '$$stage.zip'" && \
	rm -rf "$$stage"
	@echo "==> $(OUT_WINDOWS):" && ls -1 $(OUT_WINDOWS)

## desktop-linux: .deb installer + portable AppImage into output/linux (run on Linux)
desktop-linux: desktop-deps
	$(TAURI_BUILD) --bundles deb,appimage
	@mkdir -p $(OUT_LINUX)
	@for f in "$(DESKTOP_RELEASE)"/bundle/deb/*_$(DESKTOP_VERSION)_*.deb; do \
		cp "$$f" "$(OUT_LINUX)/$(DESKTOP_NAME)-linux-amd64.deb"; \
	done
	@for f in "$(DESKTOP_RELEASE)"/bundle/appimage/*_$(DESKTOP_VERSION)_*.AppImage; do \
		cp "$$f" "$(OUT_LINUX)/$(DESKTOP_NAME)-linux-amd64.AppImage" && \
		chmod +x "$(OUT_LINUX)/$(DESKTOP_NAME)-linux-amd64.AppImage"; \
	done
	@echo "==> $(OUT_LINUX):" && ls -1 $(OUT_LINUX)

## clean: remove build artefacts (not the database)
clean:
	rm -rf bin/ output/ $(TAURI_DIR)/target $(TAURI_DIR)/binaries/goeat-*

## help: list targets
help:
	@echo "Available targets:"
	@echo "  deps         - go mod tidy"
	@echo "  check        - gofmt + vet + test"
	@echo "  build        - compile to bin/goeat"
	@echo "  run          - run in development mode"
	@echo "  desktop-windows - Windows installer + portable zip -> output/windows"
	@echo "  desktop-linux   - Linux .deb + portable AppImage -> output/linux"
	@echo "  desktop-dev     - run the desktop app from source"
	@echo "  desktop-icons   - regenerate icons from desktop/icon.svg"
	@echo "  docker-up      - start the stack (no rebuild)"
	@echo "  docker-rebuild - rebuild the image and restart"
	@echo "  docker-build   - rebuild the image without starting"
	@echo "  docker-restart - restart the container"
	@echo "  docker-down    - stop and remove the stack (keeps the goeat-data volume)"
	@echo "  docker-reset   - stop the stack AND delete goeat-data (resets db + setup wizard)"
	@echo "  docker-logs    - follow container logs"
	@echo "  docker-ps      - list containers"
	@echo "  docker-backup  - archive goeat-data + .env to a zip, then trim-backup"
	@echo "  docker-restore - restore a docker-backup zip (BACKUP_FILE=...)"
	@echo "  trim-backup    - delete all but the BACKUP_KEEP most recent backups (default 3)"
	@echo "  clean        - remove build artefacts"
	@echo "  help         - show this help"
