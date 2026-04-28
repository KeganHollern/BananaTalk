IMAGE_NAME := ghcr.io/keganhollern/bananatalk-backend:latest
PLATFORM ?= linux/amd64

.PHONY: all build push test

all: build push

build:
	docker build --platform $(PLATFORM) -t $(IMAGE_NAME) .

push:
	docker push $(IMAGE_NAME)

# `make test` runs the full unit suite. Postgres-backed tests in report_test.go
# self-skip unless TEST_DATABASE_URL is exported, so this target works in
# environments without a database. Set TEST_DATABASE_URL in CI to exercise the
# auto-ban path against a real Postgres.
test:
	go test -race -count=1 ./...
