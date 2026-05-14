.PHONY: build vet test test-ci lint check ci clean schema-doc schema-doc-check

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

clean:
	rm -rf build/
