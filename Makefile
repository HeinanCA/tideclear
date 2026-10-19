.PHONY: build test lint vet

build:
	CGO_ENABLED=0 go build -o bin/tideclear ./cmd/tideclear

test:
	go test ./... -race -count=1

lint:
	golangci-lint run

vet:
	go vet ./...
