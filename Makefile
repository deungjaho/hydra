PREFIX  ?= /usr/local
BINDIR  ?= $(PREFIX)/bin
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X github.com/deungjaho/hydra/internal/cli.Version=$(VERSION) \
	-X github.com/deungjaho/hydra/internal/cli.Commit=$(COMMIT)

# 交叉编译：make build GOOS=linux GOARCH=amd64
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

.PHONY: all build install uninstall test vet fmt fmt-check clean aur-srcinfo aur-verify

all: build

build:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags="$(LDFLAGS)" -o bin/hydra ./cmd/hydra

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

# === AUR ===
# 生成 .SRCINFO（需要在 Arch Linux 上执行，或 ssh omarchy）
aur-srcinfo:
	@echo "在 omarchy 上生成 .SRCINFO..."
	scp aur/PKGBUILD omarchy:/tmp/hydra-pkgbuild-PKGBUILD
	ssh omarchy 'mkdir -p /tmp/hydra-pkgbuild && cp /tmp/hydra-pkgbuild-PKGBUILD /tmp/hydra-pkgbuild/PKGBUILD && cd /tmp/hydra-pkgbuild && makepkg --printsrcinfo > .SRCINFO && cat .SRCINFO'
	ssh omarchy 'cat /tmp/hydra-pkgbuild/.SRCINFO' > aur/.SRCINFO
	@echo "✓ aur/.SRCINFO 生成完成"

# 验证 AUR 上的版本
aur-verify:
	@curl -s "https://aur.archlinux.org/rpc/?v=5&type=info&arg=hydra-proxy" | \
		python3 -c "import sys,json; d=json.load(sys.stdin); r=d['results'][0]; print('AUR version:', r['Version'])"
