.PHONY: build test lint clean run sysop-build sysop-dev sysop-clean

build: sysop-build
	go build -o fragments-engine ./cmd/fragments-engine

sysop-build:
	@if [ ! -d apps/sysop/node_modules ]; then npm --prefix apps/sysop install; fi
	npm --prefix apps/sysop run build

sysop-dev:
	npm --prefix apps/sysop run dev

sysop-clean:
	rm -rf apps/sysop/dist apps/sysop/node_modules

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -rf ./.tmp

run:
	go run ./cmd/fragments-engine
