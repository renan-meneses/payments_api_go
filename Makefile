.PHONY: help tidy build test run consumer up down logs psql redis-cli kafka-topics migrate-up migrate-down

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

tidy: ## Download and verify dependencies (creates go.sum)
	go mod tidy

build: ## Build both binaries
	go build -trimpath -o bin/api ./cmd/api
	go build -trimpath -o bin/events-consumer ./cmd/events-consumer

test: ## Run tests with the race detector
	go test -race -count=1 ./...

run: ## Run the API locally (requires Postgres/Redis/Kafka)
	go run ./cmd/api

consumer: ## Run the events consumer locally
	go run ./cmd/events-consumer

up: ## Start the full local stack
	docker compose up --build -d

down: ## Stop the stack and remove volumes
	docker compose down -v

logs: ## Follow API logs
	docker compose logs -f api events-consumer

psql: ## Open a psql shell
	docker compose exec postgres psql -U payments -d payments

redis-cli: ## Open redis-cli
	docker compose exec redis redis-cli

kafka-topics: ## List Kafka topics
	docker compose exec kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --list

migrate-up: ## Apply migrations (local database on 5432)
	migrate -path=./migrations -database="postgres://payments:payments@localhost:5432/payments?sslmode=disable" up

migrate-down: ## Roll migrations back
	migrate -path=./migrations -database="postgres://payments:payments@localhost:5432/payments?sslmode=disable" down -all
