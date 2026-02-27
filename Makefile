BINARY  := karpx
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)" -trimpath

.PHONY: build run install release clean test snapshot

build:
	go build $(LDFLAGS) -o ./bin/$(BINARY) .

# Run directly from source — useful during development.
run:
	go run . $(ARGS)

# Install to $GOPATH/bin so `karpx` works anywhere in your terminal.
install:
	go install $(LDFLAGS) .

# Cross-platform release artifacts (matches install.sh naming).
release:
	@mkdir -p dist
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)_linux_amd64   .
	GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY)_linux_arm64   .
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)_darwin_amd64  .
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY)_darwin_arm64  .
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)_windows_amd64.exe .
	@cd dist && for f in $(BINARY)_linux_* $(BINARY)_darwin_*; do \
		tar -czf $$f.tar.gz $$f && rm $$f; \
	done
	@cd dist && zip $(BINARY)_windows_amd64.zip $(BINARY)_windows_amd64.exe \
		&& rm $(BINARY)_windows_amd64.exe
	@echo "\nArtifacts in ./dist:" && ls -lh dist/

# GoReleaser snapshot build.
snapshot:
	goreleaser release --snapshot --clean

test:
	go test ./... -race -count=1 -timeout=60s

clean:
	rm -rf ./bin ./dist
