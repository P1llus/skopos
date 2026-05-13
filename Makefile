.PHONY: build test lint clean schema-doc schema-doc-check

build:
	go build -o build/skopos ./cmd/skopos

test:
	go test ./...

lint:
	golangci-lint run ./...

schema-doc:
	go run ./tools/gen-schema-doc

schema-doc-check:
	go run ./tools/gen-schema-doc -check

clean:
	rm -rf build/
