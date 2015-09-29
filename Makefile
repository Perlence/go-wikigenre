DOCKER = docker
PACKAGE = github.com/Perlence/go-wikigenre
GOOS ?= windows
GOARCH ?= amd64
ifeq "$(GOOS)" "windows"
	ext = .exe
endif

build:
	$(DOCKER) run \
		--rm \
		-v "$(PWD)":/app/src/$(PACKAGE) \
		-w /app/src/$(PACKAGE) \
		-e GOPATH=/app \
		-e GOOS=$(GOOS) \
		-e GOARCH=$(GOARCH) \
		golang:1.5 \
		go build -v -o go-wikigenre-$(GOOS)-$(GOARCH)$(ext)

.PHONY: build
