# The single entry point for getting a checkout ready to work on and
# proving it's still healthy - Sanetizer-todo.md phase 1, item 2: "une
# commande unique... pas quinze commandes tribales transmises oralement par
# le dernier mainteneur survivant."
#
#   make bootstrap   verify the pinned Go toolchain, warm the module cache
#                     for the main module and every separate go.work module
#                     so later builds/tests need no network access
#   make verify       gofmt + go vet + go test, main module and every
#                     separate go.work module
#   make dev-image    build the reproducible dev/CI container
#                     (containers/dev/Dockerfile)
.PHONY: bootstrap verify dev-image release-check help

# go.work's own `use` block is the single source of truth for which
# directories are separate modules - read it instead of hardcoding the
# list a second time, so this can't silently drift from go.work.
MODULES := $(shell awk '/^use \(/{flag=1; next} /^\)/{flag=0} flag {gsub(/^[ \t]+/, ""); if ($$0 != ".") print}' go.work)

help:
	@echo "make bootstrap      - verify the Go toolchain, warm module cache for offline builds"
	@echo "make verify         - gofmt/vet/test the main module and every separate go.work module"
	@echo "make dev-image      - build the reproducible dev/CI container (containers/dev/Dockerfile)"
	@echo "make release-check  - run the stabilization verification workflow end to end"

bootstrap:
	scripts/bootstrap-dev.sh

verify:
	test -z "$$(find api cmd conformance examples internal plugins sdk tests -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
	go build ./...
	go vet ./...
	go test ./...
	@for module in $(MODULES); do \
		echo "verifying $$module (GOWORK=off)..." >&2; \
		(cd $$module && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...) || exit 1; \
	done

dev-image:
	podman build -t platform-factory-dev -f containers/dev/Dockerfile containers/dev

release-check:
	scripts/release-check.sh
