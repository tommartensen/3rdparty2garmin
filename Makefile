BINARY := 3rdparty2garmin

.PHONY: all build test lint fmt run clean

all: lint test build

build:
	go build -o $(BINARY) .

test:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

run:
	go run .

clean:
	rm -f $(BINARY)
