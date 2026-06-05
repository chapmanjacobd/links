.PHONY: build release-build test cover clean fmt lint install all

BINARY_NAME=links
DIST_DIR?=dist
BUILD_TAGS?=
LDFLAGS?=-s -w
GOOS?=$(shell go env GOOS)
GOARCH?=$(shell go env GOARCH)
GO_BUILD_FLAGS=-trimpath
ifneq ($(strip $(BUILD_TAGS)),)
GO_BUILD_FLAGS += -tags '$(BUILD_TAGS)'
endif
GO_BUILD_FLAGS += -ldflags '$(LDFLAGS)'
ifeq ($(GOOS),windows)
RELEASE_EXT=.exe
endif
RELEASE_BINARY=$(DIST_DIR)/$(BINARY_NAME)-$(GOOS)-$(GOARCH)$(RELEASE_EXT)

all: fmt lint test build

build:
	go build $(GO_BUILD_FLAGS) -o $(BINARY_NAME)

release-build:
	mkdir -p $(DIST_DIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(GO_BUILD_FLAGS) -o $(RELEASE_BINARY)

test:
	go test ./...

cover:
	go test  -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | awk '{n=split($$NF,a,"%%"); if (a[1] < 85) print $$0}' | sort -k3 -n

fmt:
	gofmt -s -w -e .
	go fix  ./...
	-goimports -w -e .
	-gofumpt -w .
	-gci write .

lint:
	-staticcheck  ./...
	go vet  ./...

clean:
	rm -f $(BINARY_NAME)
	rm -f coverage.out
	rm -rf $(DIST_DIR)

install:
	go install $(GO_BUILD_FLAGS) .
