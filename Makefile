.PHONY: build test run-mock run-proxy clean

BIN_DIR := bin

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/firerouter ./cmd/firerouter
	go build -o $(BIN_DIR)/mockupstream ./cmd/mockupstream
	go build -o $(BIN_DIR)/loadgen ./cmd/loadgen

test:
	go vet ./...
	go test -race ./...

run-mock: build
	./$(BIN_DIR)/mockupstream

run-proxy: build
	./$(BIN_DIR)/firerouter

clean:
	rm -rf $(BIN_DIR)
