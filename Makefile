.PHONY: build vet test test-ci lint check ci clean schema-doc schema-doc-check integration-tests integration-tests-update

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

integration-tests:
	go test -tags=integration ./cmd/skopos -run TestScripts -count=1 -v

integration-tests-update:
	go test -tags=integration ./cmd/skopos -run TestScripts -count=1 -v -args -update

clean:
	rm -rf build/
