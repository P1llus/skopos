.PHONY: build vet test test-ci lint check ci clean schema-doc schema-doc-check json-schema json-schema-check integration-tests integration-tests-update bench bench-go bench-clean

build:
	go build -o build/skopos ./cmd/skopos

vet:
	go vet ./...

test:
	go test ./...

test-ci:
	go test ./... -race -count=1

lint:
	golangci-lint run ./...

check: vet build test lint

ci: vet build test-ci lint

schema-doc:
	go run ./tools/gen-schema-doc

schema-doc-check:
	go run ./tools/gen-schema-doc -check

json-schema:
	go run ./tools/gen-json-schema

json-schema-check:
	go run ./tools/gen-json-schema -check

integration-tests:
	go test -tags=integration ./cmd/skopos -run TestScripts -count=1 -v

integration-tests-update:
	go test -tags=integration ./cmd/skopos -run TestScripts -count=1 -v -args -update

clean:
	rm -rf build/

# bench-go runs the Go-level benchmarks (testing.B) for per-scenario
# ns/op + allocs and sequential-vs-concurrent fleet sweeps. Override
# BENCH_TIME for longer/shorter runs (e.g. BENCH_TIME=2s for tighter
# variance, BENCH_TIME=100ms for a quick smoke).
BENCH_TIME ?= 1s
bench:
	go test -bench=. -benchmem -benchtime=$(BENCH_TIME) ./bench

bench-clean:
	rm -rf bench/out/
