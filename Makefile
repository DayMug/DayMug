# Developer entry points. Every pnpm call runs from frontend/: outside it the
# corepack shim has no packageManager pin and picks a pnpm that can't read
# the lockfile (see AGENTS.md).

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Same flags as the release workflow's build step. The release additionally
# overrides service.DefaultManifestURL with its own GitHub Releases manifest and UPX-packs
# linux; a local build keeps the compiled-in manifest and skips UPX.
LDFLAGS := -s -w -X main.Version=$(VERSION)

DEV_DATA := data

.PHONY: build dev-init dev-backend dev-frontend test-fast clean

frontend/node_modules: frontend/package.json frontend/pnpm-lock.yaml
	cd frontend && pnpm install --frozen-lockfile
	@touch $@

# Release-style single binary at backend/bin/daymug with the frontend
# embedded. It lives next to air's dev binary, so it shares backend/bin/data
# (the dev database) — fine for a smoke run; copy it elsewhere to deploy.
# Like the release, JS/CSS are embedded gzip-only: the static handler serves
# the .gz with Content-Encoding: gzip, and -n keeps the bytes reproducible.
build: frontend/node_modules
	cd frontend && APP_VERSION=$(VERSION) pnpm build
	rm -rf backend/cmd/server/dist
	cp -r frontend/dist backend/cmd/server/dist
	find backend/cmd/server/dist/assets -type f \( -name '*.js' -o -name '*.css' \) \
		-exec gzip -9 -n {} +
	cd backend && CGO_ENABLED=0 go build -trimpath -tags embed_frontend \
		-ldflags "$(LDFLAGS)" -o bin/daymug ./cmd/server
	@echo "built backend/bin/daymug ($(VERSION))"

# One-time dev config at <repo>/data/config.yaml — the file backend/.air.toml
# reads — written by `server init` exactly as docs/guides/installation.md describes.
# Re-running leaves an existing config alone.
dev-init:
	@if [ -f $(DEV_DATA)/config.yaml ]; then \
		echo "$(DEV_DATA)/config.yaml already exists; leaving it alone"; \
	else \
		cd backend && go build -o bin/server ./cmd/server && cd .. && \
		mkdir -p $(DEV_DATA) && cd $(DEV_DATA) && ../backend/bin/server init; \
	fi

dev-backend: dev-init
	cd backend && air

dev-frontend: frontend/node_modules
	cd frontend && pnpm dev

test-fast: frontend/node_modules
	./scripts/test.sh fast

# Build outputs only. backend/bin/data (the dev database) and <repo>/data
# (the dev config) are state, not artifacts, and survive a clean.
clean:
	rm -rf frontend/dist backend/cmd/server/dist backend/tmp
	rm -f backend/bin/daymug backend/bin/server
