IMAGE_NAME := ghcr.io/keganhollern/bananatalk-backend:latest
PLATFORM ?= linux/amd64

.PHONY: all build push

all: build push

build:
	docker build --platform $(PLATFORM) -t $(IMAGE_NAME) .

push:
	docker push $(IMAGE_NAME)
