APP_NAME := kubeflare

.PHONY: tidy test build run

tidy:
	go mod tidy

test:
	mkdir -p .cache/go-build .cache/go-mod
	GOCACHE=$(PWD)/.cache/go-build GOMODCACHE=$(PWD)/.cache/go-mod go test ./...

build:
	go build ./cmd/kubeflare

run:
	go run ./cmd/kubeflare serve --config ./configs/config.example.yaml
