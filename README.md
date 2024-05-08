# Terraform Operator API

The Terraform Operator API provides endpoints for managing infrastructure resources using Terraform. This README explains how to set up the development environment and configure the server.

## Prerequisites

Before running the server, make sure you have the following prerequisites installed:

- Go (version 1.16 or higher)
- Git
- Docker (optional, for containerized development)

## Installation

1. Clone the repository:

   `git clone https://github.com/GalleyBytes/terraform-operator-api.git `

2. Navigate to the project directory:

   `cd terraform-operator-api `

3. Install Go dependencies:

   `go mod download `

## Configuration

### Environment Variables

Export the following environment variables

- `KUBECONFIG`: The kubernetes cluster API config (In-cluster is used when running in a Kubernetes pod)
- `ADDR`: The address on which the server will listen (default: `0.0.0.0:3000`)
- `DB_URL`: Connection string for your database (if applicable)

### Flags

You can also configure the server using command-line flags:

- `--addr`: Specify the address (e.g., `--addr ':3000'`)
- `--db-url`: Specify the database connection string (e.g., `--db-url postgres://user:password@localhost/dbname`)

## Running the Server

To start the API server, run:

`go run cmd/main.go `

The server will be accessible at `http://localhost:3000` (or the specified port).

## Testing

Use tools like `curl` or Postman to test the API endpoints. Verify that the responses match your expectations.