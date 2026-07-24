BIN_DIR := bin
BINARY := $(BIN_DIR)/tidy-wow

.PHONY: build test clean

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BINARY) ./cmd/tidy-wow

test:
	go vet ./...
	go test ./...

clean:
	rm -rf $(BIN_DIR)
