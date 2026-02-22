.PHONY: build run test

build:
	go build -o bin/mishri cmd/mishri/main.go

run:
	go run cmd/mishri/main.go

test:
	go test ./...
