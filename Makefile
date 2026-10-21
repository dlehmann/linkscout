# LinkScout build helpers. Requires Go >= 1.24.
# Version from the latest tag (v1.2.3 → 1.2.3); "dev" outside a git checkout.
VERSION ?= $(or $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//'),dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PREFIX  ?= /usr/local

.PHONY: build test race vet fmt install uninstall release clean

# Static binary (no cgo, no libc dependency) for the current system.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o linkscout .

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

fmt:
	gofmt -w .

# Binary plus desktop launcher and icon. Without root:
#   make install PREFIX=$$HOME/.local
install: build
	install -Dm755 linkscout $(DESTDIR)$(PREFIX)/bin/linkscout
	install -Dm644 packaging/linkscout.desktop $(DESTDIR)$(PREFIX)/share/applications/linkscout.desktop
	install -Dm644 packaging/linkscout.svg $(DESTDIR)$(PREFIX)/share/icons/hicolor/scalable/apps/linkscout.svg
	-update-desktop-database $(DESTDIR)$(PREFIX)/share/applications 2>/dev/null
	-gtk-update-icon-cache -q $(DESTDIR)$(PREFIX)/share/icons/hicolor 2>/dev/null

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/linkscout
	rm -f $(DESTDIR)$(PREFIX)/share/applications/linkscout.desktop
	rm -f $(DESTDIR)$(PREFIX)/share/icons/hicolor/scalable/apps/linkscout.svg

# Cross-compiled release binaries in dist/ (the same set the GitHub release
# workflow publishes): Linux x86_64 and arm64, macOS Apple Silicon.
RELEASE_TARGETS := linux/amd64 linux/arm64 darwin/arm64

release:
	mkdir -p dist
	@for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o dist/linkscout-$$os-$$arch . || exit 1; \
	done

clean:
	rm -rf linkscout dist
