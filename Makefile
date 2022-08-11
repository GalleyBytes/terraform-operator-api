build:
	GOOS=linux go build -o server cmd/main.go
server:
	go run cmd/main.go

.PHONY: server
