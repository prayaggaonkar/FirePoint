.PHONY: build test compat run-mock run-proxy clean demo-up demo-down

BIN_DIR := bin

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/firerouter ./cmd/firerouter
	go build -o $(BIN_DIR)/mockupstream ./cmd/mockupstream
	go build -o $(BIN_DIR)/loadgen ./cmd/loadgen

test:
	go vet ./...
	go test -race ./...

compat:
	./compat/run.sh

run-mock: build
	./$(BIN_DIR)/mockupstream

run-proxy: build
	./$(BIN_DIR)/firerouter

clean:
	rm -rf $(BIN_DIR)

COMPOSE := docker compose $(if $(wildcard .env),--env-file .env,) -f deploy/docker-compose.yml

demo-venv:
	@if ! demo/.venv/bin/python -c 'import openai' >/dev/null 2>&1; then \
		python3 -m venv demo/.venv; \
		demo/.venv/bin/pip install -q -r compat/python/requirements.txt; \
	fi

demo-up:
	$(MAKE) demo-venv >/dev/null & pid=$$!; \
	$(COMPOSE) up --build -d; \
	status=$$?; \
	wait $$pid || status=$$?; \
	exit $$status

demo-down:
	$(COMPOSE) --profile load down --remove-orphans
