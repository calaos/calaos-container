GOCMD=go
GOTEST=$(GOCMD) test
GOVET=$(GOCMD) vet
BINARY_NAME=calaos-container
BINARY_NAME_TOOL=calaos-os
VERSION?=1.0.0

TOP_DIR := $(dir $(abspath $(firstword $(MAKEFILE_LIST))))
SUBDIRS := apt

SERVER_LDFLAGS := -L$(pwd)/bin -L. -L./bin -lcalaos-apt

GREEN  := $(shell tput -Txterm setaf 2)
YELLOW := $(shell tput -Txterm setaf 3)
WHITE  := $(shell tput -Txterm setaf 7)
CYAN   := $(shell tput -Txterm setaf 6)
RESET  := $(shell tput -Txterm sgr0)

.PHONY: all test build $(SUBDIRS)
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
	rm -fr ./out
	rm -f ./junit-report.xml checkstyle-report.xml ./coverage.xml ./profile.cov yamllint-checkstyle.xml

## Test:
test: ## Run the tests of the project
#	$(GOTEST) -v -race ./... $(OUTPUT_OPTIONS)
	@echo test disabled

coverage: ## Run the tests of the project and export the coverage
	$(GOTEST) -cover -covermode=count -coverprofile=profile.cov ./...
	$(GOCMD) tool cover -func profile.cov

install-lib: apt-install

apt-install:
	$(MAKE) -C apt install

install: install-lib ## Install the binaries
	install -Dm755 bin/$(BINARY_NAME) $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME)
	install -Dm755 bin/$(BINARY_NAME_TOOL) $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME_TOOL)
	install -Dm755 scripts/start_calaos_home.sh $(DESTDIR)$(PREFIX)/sbin/start_calaos_home.sh
	install -Dm755 scripts/calaos_install.sh $(DESTDIR)$(PREFIX)/sbin/calaos_install.sh
	install -Dm755 scripts/calaos_rollback.sh $(DESTDIR)$(PREFIX)/sbin/calaos_rollback.sh
	install -Dm755 scripts/init_calaosfs.sh $(DESTDIR)$(PREFIX)/sbin/init_calaosfs.sh
	install -Dm755 scripts/haproxy_pre.sh $(DESTDIR)$(PREFIX)/sbin/haproxy_pre.sh
	install -Dm755 scripts/mosquitto_pre.sh $(DESTDIR)$(PREFIX)/sbin/mosquitto_pre.sh
	install -Dm755 scripts/load_containers_cache.sh $(DESTDIR)$(PREFIX)/sbin/load_containers_cache.sh
	install -Dm755 scripts/arch-chroot $(DESTDIR)$(PREFIX)/sbin/arch-chroot
	install -Dm755 scripts/genfstab $(DESTDIR)$(PREFIX)/sbin/genfstab