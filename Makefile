.PHONY: gen lint test build install-plugin clean buf-push

BIN_DIR := bin
PLUGIN  := $(BIN_DIR)/protoc-gen-mcp

gen:
	buf generate

lint:
	buf lint
	go vet ./...
	golangci-lint run --timeout 5m ./...

test:
	go test -race ./...

build: $(PLUGIN)

$(PLUGIN):
	mkdir -p $(BIN_DIR)
	go build -o $(PLUGIN) ./cmd/protoc-gen-mcp

install-plugin:
	go install ./cmd/protoc-gen-mcp

clean:
	rm -rf $(BIN_DIR)

# Publishes the annotation schema module to BSR. `buf build` first as a
# sanity check (catches proto errors before any network call). The
# --exclude-unnamed flag skips the proto/examples workspace module,
# which has no name and must not reach BSR — see CONTRIBUTING.md for
# the full story.
buf-push:
	buf build
	buf push --exclude-unnamed
