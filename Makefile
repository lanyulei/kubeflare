APP_NAME := kubeflare
MIGRATIONS_DIR ?= migrations/postgres

ifneq (,$(wildcard .env))
include .env
export
endif

DATABASE_DSN ?= $(KUBEFLARE_DATABASE__DSN)

.PHONY: tidy test build run migrate

tidy:
	go mod tidy

test:
	mkdir -p .cache/go-build .cache/go-mod
	GOCACHE=$(PWD)/.cache/go-build GOMODCACHE=$(PWD)/.cache/go-mod go test ./...

build:
	go build ./cmd/kubeflare

run:
	go run ./cmd/kubeflare serve --config ./configs/config.example.yaml

migrate:
	@if [ -z "$(DATABASE_DSN)" ]; then \
		echo "DATABASE_DSN is required. Usage: make migrate DATABASE_DSN='postgres://kubeflare:password@127.0.0.1:5432/kubeflare?sslmode=disable'"; \
		exit 1; \
	fi
	psql "$(DATABASE_DSN)" -v ON_ERROR_STOP=1 -c "CREATE TABLE IF NOT EXISTS schema_migration (version VARCHAR(255) PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW());"
	psql "$(DATABASE_DSN)" -v ON_ERROR_STOP=1 -c "INSERT INTO schema_migration (version) SELECT '000001_init' WHERE EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('iam_user', 'cluster')) ON CONFLICT (version) DO NOTHING; INSERT INTO schema_migration (version) SELECT '000002_soft_delete_and_indexes' WHERE EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'iam_user' AND column_name = 'deleted_at') AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'cluster' AND column_name = 'deleted_at') ON CONFLICT (version) DO NOTHING; INSERT INTO schema_migration (version) SELECT '000003_user_management_rebuild' WHERE EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'iam_user' AND column_name = 'username') ON CONFLICT (version) DO NOTHING; INSERT INTO schema_migration (version) SELECT '000004_auth_security_features' WHERE EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'iam_auth_session') AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'iam_user' AND column_name = 'mfa_enabled') ON CONFLICT (version) DO NOTHING;"
	@for file in $(MIGRATIONS_DIR)/*.up.sql; do \
		version=$$(basename "$$file" .up.sql); \
		applied=$$(psql "$(DATABASE_DSN)" -v ON_ERROR_STOP=1 -tAc "SELECT 1 FROM schema_migration WHERE version = '$$version'"); \
		if [ "$$applied" = "1" ]; then \
			echo "skip $$version"; \
		else \
			echo "apply $$version"; \
			psql "$(DATABASE_DSN)" -v ON_ERROR_STOP=1 -f "$$file"; \
			psql "$(DATABASE_DSN)" -v ON_ERROR_STOP=1 -c "INSERT INTO schema_migration (version) VALUES ('$$version') ON CONFLICT (version) DO NOTHING;"; \
		fi; \
	done
