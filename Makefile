GOCMD=go
GOTEST=$(GOCMD) test
GOVET=$(GOCMD) vet
BINARY_NAME=calaos-container
BINARY_NAME_TOOL=calaos-os
PREFIX ?= /usr

# The git tag is the only version authority. CI passes the freshly bumped tag
# in CALAOS_VERSION because the tag does not exist yet when the package builds.
CALAOS_VERSION ?= $(shell git describe --tags 2>/dev/null || { h=$$(git rev-parse --short HEAD 2>/dev/null) && echo 0.0.0-0-g$$h; } || echo 0.0.0)
# Debian ordering: 1.8.1-dev.2 -> 1.8.1~dev.2 (sorts before 1.8.1), and an
# untagged commit 1.8.0-3-gabc -> 1.8.0+3.gabc (sorts after 1.8.0).
DEB_UPSTREAM_VERSION = $(shell echo '$(CALAOS_VERSION)' | sed -e 's/-\([0-9]\+\)-g\([0-9a-f]\+\)$$/+\1.g\2/' -e 's/-/~/g')

TOP_DIR := $(dir $(abspath $(firstword $(MAKEFILE_LIST))))
SUBDIRS := apt

# Absolute: go test links from each package dir. The lib is also installed
# under /usr by build-lib, which is what the link has relied on so far.
SERVER_LDFLAGS := -L$(CURDIR)/apt/build -lcalaos-apt

GREEN  := $(shell tput -Txterm setaf 2)
YELLOW := $(shell tput -Txterm setaf 3)
WHITE  := $(shell tput -Txterm setaf 7)
CYAN   := $(shell tput -Txterm setaf 6)
RESET  := $(shell tput -Txterm sgr0)

.PHONY: all test build version deb-version $(SUBDIRS)
.ONESHELL:

all: build

## Help:
help: ## Show this help.
	@echo ''
	@echo 'Usage:'
	@echo '  ${YELLOW}make${RESET} ${GREEN}<target>${RESET}'
	@echo ''
	@echo 'Targets:'
	@awk 'BEGIN {FS = ":.*?## "} { \
		if (/^[a-zA-Z_-]+:.*?##.*$$/) {printf "    ${YELLOW}%-20s${GREEN}%s${RESET}\n", $$1, $$2} \
		else if (/^## .*$$/) {printf "  ${CYAN}%s${RESET}\n", substr($$1,4)} \
		}' $(MAKEFILE_LIST)

## Build:
build: build-lib build-server build-tools ## Build the project and put the output binary in bin/
	@mkdir -p bin

build-tools:
	@mkdir -p bin
	@cd cmd/calaos-os
	$(GOCMD) build -v -o $(TOP_DIR)/bin/$(BINARY_NAME_TOOL) .
	@cd $(TOP_DIR)

build-server: build-lib
	@mkdir -p bin
	CGO_LDFLAGS="$(SERVER_LDFLAGS)" $(GOCMD) build -v -o bin/$(BINARY_NAME) .

build-lib: $(SUBDIRS)

$(SUBDIRS):
	$(MAKE) -C $@

clean: ## Remove build related file
	rm -fr ./bin
	rm -f ./profile.cov

version: ## Print the version derived from git
	@echo $(CALAOS_VERSION)

deb-version: ## Print the Debian upstream version derived from git
	@echo $(DEB_UPSTREAM_VERSION)

## Test:
test: build-lib ## Run the tests of the project
	CGO_LDFLAGS="$(SERVER_LDFLAGS)" $(GOTEST) -race ./...

coverage: ## Run the tests of the project and export the coverage
	$(GOTEST) -cover -covermode=count -coverprofile=profile.cov ./...
	$(GOCMD) tool cover -func profile.cov

install-lib: apt-install

apt-install:
	$(MAKE) -C apt install

install: install-lib ## Install the binaries
	install -Dm755 bin/$(BINARY_NAME) $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME)
	install -Dm755 bin/$(BINARY_NAME_TOOL) $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME_TOOL)
	install -Dm644 scripts/lib/calaos-common.sh $(DESTDIR)$(PREFIX)/lib/calaos/calaos-common.sh
	install -Dm755 scripts/calaos_install $(DESTDIR)$(PREFIX)/sbin/calaos_install
	install -Dm755 scripts/calaos_rollback $(DESTDIR)$(PREFIX)/sbin/calaos_rollback
	install -Dm755 scripts/init_calaosfs $(DESTDIR)$(PREFIX)/sbin/init_calaosfs
	install -Dm755 scripts/load_containers_cache $(DESTDIR)$(PREFIX)/sbin/load_containers_cache
	install -Dm755 scripts/arch-chroot $(DESTDIR)$(PREFIX)/sbin/arch-chroot
	install -Dm755 scripts/genfstab $(DESTDIR)$(PREFIX)/sbin/genfstab
	install -Dm755 scripts/pull_calaos_image $(DESTDIR)$(PREFIX)/sbin/pull_calaos_image
	install -Dm755 scripts/config_calaos-boot $(DESTDIR)$(PREFIX)/sbin/config_calaos-boot
