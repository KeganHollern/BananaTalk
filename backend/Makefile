IMAGE_NAME := ghcr.io/keganhollern/bananatalk-backend:latest

.PHONY: all build push

all: build push

build:
	docker build -t $(IMAGE_NAME) .

push:
	docker push $(IMAGE_NAME)
