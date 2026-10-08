.PHONY: dev frontend-build build test test-integration fmt lint migrate migrate-down migrate-status migrate-create admin rename-module feature verify

dev:
	@test -x ./node_modules/.bin/tailwindcss || (echo "run 'npm install' first"; exit 1)
	@test -x ./node_modules/.bin/esbuild || (echo "run 'npm install' first"; exit 1)
	@set -eu; \
		tailwind_pid=; \
		esbuild_pid=; \
		cleanup() { \
			trap - EXIT INT TERM; \
			[ -z "$$tailwind_pid" ] || kill "$$tailwind_pid" 2>/dev/null || true; \
			[ -z "$$esbuild_pid" ] || kill "$$esbuild_pid" 2>/dev/null || true; \
			[ -z "$$tailwind_pid" ] || wait "$$tailwind_pid" 2>/dev/null || true; \
			[ -z "$$esbuild_pid" ] || wait "$$esbuild_pid" 2>/dev/null || true; \
		}; \
		trap 'status=$$?; cleanup; exit $$status' EXIT; \
		trap 'exit 130' INT; \
		trap 'exit 143' TERM; \
		./node_modules/.bin/tailwindcss -i ./web/src/css/app.css -o ./web/static/css/app.css --watch=always & tailwind_pid=$$!; \
		./node_modules/.bin/esbuild ./web/src/js/app.js --bundle --sourcemap --target=es2020 --outfile=./web/static/js/app.js --watch=forever & esbuild_pid=$$!; \
		go tool air

frontend-build:
	npm run build

build: frontend-build
	mkdir -p bin
	go build -trimpath -o bin/app ./cmd/app
	go build -trimpath -o bin/migrate ./cmd/migrate

test:
	go test ./...

test-integration:
	go test -tags=integration ./...

fmt:
	go fmt ./...

lint:
	go vet ./...

migrate:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down

migrate-status:
	go run ./cmd/migrate status

migrate-create:
	@test -n "$(name)" || (echo "usage: make migrate-create name=create_users"; exit 1)
	go run ./cmd/migrate create "$(name)"

admin:
	go run ./cmd/app admin create

rename-module:
	@test -n "$(module)" || (echo "usage: make rename-module module=github.com/example/project"; exit 1)
	go run ./cmd/rename-module -module "$(module)"

feature:
	@test -n "$(name)" || (echo "usage: make feature name=customers"; exit 1)
	go run ./cmd/feature -name "$(name)"

verify: frontend-build
	go vet ./...
	go test ./...
	go build ./...
