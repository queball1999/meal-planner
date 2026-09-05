BINARY  = bin/goeat
GOWORK ?= off

.PHONY: all deps check build run clean

all: check build

## deps: tidy go modules
deps:
	GOWORK=$(GOWORK) go mod tidy

## check: fmt + vet + test (definition of done for each phase)
check: deps
	@echo "==> gofmt"
	@test -z "$$(GOWORK=$(GOWORK) gofmt -l .)" || \
		(echo "ERROR: files need formatting — run: gofmt -w ." && exit 1)
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

## clean: remove build artefacts (not the database)
clean:
	rm -rf bin/
