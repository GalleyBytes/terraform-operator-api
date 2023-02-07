DOCKER_REPO ?= ghcr.io/galleybytes
IMAGE_NAME ?= terraform-operator-api
VERSION ?= $(shell  git describe --tags --dirty)
ifeq ($(VERSION),)
VERSION := 0.0.0
endif
IMG ?= ${DOCKER_REPO}/${IMAGE_NAME}:${VERSION}

release: build
	docker build . -t ${IMG}
	docker push ${IMG}

build:
	GOOS=linux GOARCH=amd64 go build -v -installsuffix cgo -o bin/server cmd/main.go

server:
	go run cmd/main.go

.PHONY: server release
