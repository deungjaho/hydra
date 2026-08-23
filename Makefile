PREFIX  ?= /usr/local
BINDIR  ?= $(PREFIX)/bin
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X github.com/deungjaho/hydra/internal/cli.Version=$(VERSION) \
	-X github.com/deungjaho/hydra/internal/cli.Commit=$(COMMIT)

.PHONY: all build install uninstall test vet fmt clean

all: build

build:
	go build -ldflags="$(LDFLAGS)" -o bin/hydra ./cmd/hydra

install: build
	install -d $(DESTDIR)$(BINDIR)
	install -m 0755 bin/hydra $(DESTDIR)$(BINDIR)/hydra

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/hydra

test:
	go test -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w .
	goimports -w .

fmt-check:
	@diff=$$(gofmt -l .); if [ -n "$$diff" ]; then \
		echo "gofmt found unformatted files:"; echo "$$diff"; exit 1; fi

clean:
	rm -rf bin/
