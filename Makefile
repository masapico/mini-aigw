.PHONY: all build build-windows build-linux test run clean

APP_NAME := mini-aigw
CMD_DIR := ./cmd/server
DIST_DIR := dist
BIN_DIR := bin

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -v -o $(BIN_DIR)/$(APP_NAME) $(CMD_DIR)

build-windows:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o $(DIST_DIR)/$(APP_NAME).exe $(CMD_DIR)
	@echo "Built Windows binary: $(DIST_DIR)/$(APP_NAME).exe"

build-linux:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(DIST_DIR)/$(APP_NAME) $(CMD_DIR)
	@echo "Built Linux binary: $(DIST_DIR)/$(APP_NAME)"

test:
	go test -v ./...

run:
	go run $(CMD_DIR)

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)

