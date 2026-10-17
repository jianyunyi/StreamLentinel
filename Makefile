.PHONY: all fmt vet lint test race cover fuzz clean

GO ?= go
FUZZTIME ?= 30s

all: fmt vet race

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

test:
	$(GO) test ./...

# Race detector on: the merge gate runs this, not plain `test`.
race:
	$(GO) test -race -count=1 ./...

cover:
	$(GO) test -race -coverprofile=coverage.out ./internal/codec/...
	$(GO) tool cover -func=coverage.out | tail -n 1

# Runs every fuzz target for $(FUZZTIME). Corpus files that found bugs are kept
# under testdata/fuzz/<Target>/ and replayed by plain `go test`.
fuzz:
	$(GO) test ./internal/codec/ws  -run='^$$' -fuzz='^FuzzDecoder$$'             -fuzztime=$(FUZZTIME)
	$(GO) test ./internal/codec/ws  -run='^$$' -fuzz='^FuzzUTF8Chunking$$'        -fuzztime=$(FUZZTIME)
	$(GO) test ./internal/codec/ws  -run='^$$' -fuzz='^FuzzAssembler$$'           -fuzztime=$(FUZZTIME)
	$(GO) test ./internal/codec/sse -run='^$$' -fuzz='^FuzzChunkingIndependence$$' -fuzztime=$(FUZZTIME)

clean:
	rm -f coverage.out
