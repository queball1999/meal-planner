BINARY  = bin/goeat
GOWORK ?= off

.PHONY: all deps check build run clean \
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

## clean: remove build artefacts (not the database)
clean:
	rm -rf bin/

## help: list targets
help:
	@echo "Available targets:"
	@echo "  deps         - go mod tidy"
	@echo "  check        - gofmt + vet + test"
	@echo "  build        - compile to bin/goeat"
	@echo "  run          - run in development mode"
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
