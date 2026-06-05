.PHONY: build test cover clean fmt lint install all

BINARY_NAME=links
BUILD_TAGS?=
LDFLAGS?=-s -w
GO_BUILD_FLAGS=-trimpath
ifneq ($(strip $(BUILD_TAGS)),)
GO_BUILD_FLAGS += -tags '$(BUILD_TAGS)'
endif
GO_BUILD_FLAGS += -ldflags '$(LDFLAGS)'

all: fmt lint test build

build:
	go build $(GO_BUILD_FLAGS) -o $(BINARY_NAME)

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

install:
	go install $(GO_BUILD_FLAGS) .
