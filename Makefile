# Thin aliases only. Canonical logic lives in scripts/.
.PHONY: dev-up dev-down dev-reset migrate generate test check build

dev-up:
	./scripts/dev-up.sh

dev-down:
	./scripts/dev-down.sh

dev-reset:
	./scripts/dev-reset.sh

migrate:
	./scripts/migrate.sh up

generate:
	./scripts/generate.sh

test:
	./scripts/test.sh

check:
	./scripts/check.sh

build:
	go build -trimpath -o bin/moonlight-cloud ./cmd/moonlight-cloud
