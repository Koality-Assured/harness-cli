BINARY_NAME=harness
CMD_PATH=./cmd/harness
VERSION?=0.1.0
LDFLAGS=-ldflags="-s -w -X main.version=$(VERSION)"

.PHONY: all build test lint clean release-dry-run

all: test build

build:
	go build $(LDFLAGS) -o bin/$(BINARY_NAME) $(CMD_PATH)

test:
	go test -v -race ./...

test-short:
	go test -v -short ./...

clean:
	rm -rf bin/ dist/ *.exe coverage.txt

lint:
	go vet ./...

cross-compile:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-amd64.exe $(CMD_PATH)
	GOOS=windows GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-arm64.exe $(CMD_PATH)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-arm64 $(CMD_PATH)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 $(CMD_PATH)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 $(CMD_PATH)
