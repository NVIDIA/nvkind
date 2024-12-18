GO_VERSION=1.22.1
MODULE="github.com/NVIDIA/nvkind"

all: vendor fmt build

TARGETS := vendor fmt lint vet test build run
.PHONY: $(TARGETS)

fmt:
	go list -f '{{.Dir}}' $(MODULE)/... \
		| xargs gofmt -s -l -w

lint:
	golangci-lint run ./...

vet:
	go vet $(MODULE)/...

vendor:
	go mod tidy
	go mod vendor
	go mod verify

test:
	go test $(MODULE)/...

build:
	go build $(MODULE)/cmd/...

run:
	go run $(MODULE)/...

check-vendor: vendor
	@echo "- Checking if go.mod and go.sum are in sync..."
	@git diff --exit-code -- go.sum go.mod
	@echo "- Checking if the go mod vendor dir is in sync..."
	@git diff --exit-code -- vendor
