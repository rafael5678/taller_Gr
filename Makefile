.PHONY: proto build test up down

proto:
	buf dep update
	buf generate
	buf build proto --as-file-descriptor-set -o deploy/envoy/descriptor.pb

build: proto
	go build ./...

test: proto
	go test ./...

up: proto
	docker compose -f deploy/docker-compose.yml up --build

down:
	docker compose -f deploy/docker-compose.yml down
