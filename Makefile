.PHONY: build test lint clean run

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -rf ./.tmp

run:
	go run ./cmd/fragments-engine
