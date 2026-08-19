# LedgerCore — convenience targets. See RESULTS.md for the exact reproduce path.

# Defaults match the native Homebrew Postgres used for the committed results.
export DATABASE_URL      ?= postgres://localhost:5432/ledgercore?sslmode=disable
export TEST_DATABASE_URL ?= postgres://localhost:5432/ledgercore_test?sslmode=disable

.PHONY: db-native db-docker migrate test race bench bench-contention serve build tidy fmt vet clean

## Create the two native Postgres databases (Homebrew Postgres already running).
db-native:
	-createdb ledgercore
	-createdb ledgercore_test

## Bring up Postgres via Docker instead (functionally equivalent).
db-docker:
	docker compose up -d
	@echo "Set DATABASE_URL/TEST_DATABASE_URL to the ledger:ledger@localhost creds (see docker-compose.yml)."

## Build both binaries.
build:
	go build -o bin/ledgerd ./cmd/ledgerd
	go build -o bin/loadgen ./cmd/loadgen

## Full test suite with the race detector (packages serialized: shared test DB).
race:
	go test -race -p 1 -count=1 ./...

## Test suite without race (faster).
test:
	go test -p 1 -count=1 ./...

## Headline load + invariant run -> results/load.json, results/summary.json.
bench:
	go run ./cmd/loadgen -workers 32 -ops 3000 -accounts 32 -seed 42 -idem-trials 500 -idem-conc 32 -out results

## Adversarial high-contention drain run (forces the no-negative constraint).
bench-contention:
	go run ./cmd/loadgen -workers 64 -ops 1500 -accounts 4 -initial 5000 -seed 7 -idem-trials 100 -idem-conc 32 -out /tmp/lc_contention

## Run the HTTP service (:8080).
serve:
	go run ./cmd/ledgerd

fmt:   ; gofmt -w ./cmd ./internal ./migrations
vet:   ; go vet ./...
tidy:  ; go mod tidy
clean: ; rm -rf bin
