.PHONY: build test lint clean run seed-config serve-api sysop-build sysop-dev sysop-clean

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

# Seed the gitignored runtime config (fragments.yaml) from the committed
# template. Idempotent: a no-op once the runtime config exists.
seed-config:
	./scripts/seed-config.sh

# Build, seed the runtime config, and start the HTTP API against it.
# The live server must use fragments.yaml, never the fragments.example.yaml
# template, because the ingest CRUD endpoints rewrite the config file in place.
serve-api: build seed-config
	./fragments-engine serve-api --addr :8091 --config fragments.yaml
