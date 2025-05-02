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



# Setting up a Full Development Environment

## Prerequisites

### Kubernetes Cluster

There are a variety of methods to run a local Kubernetes cluster. Pick your favorite and ensure you can access it. 

### Database setup with Postgres

Start a postgres database. Example:

```bash
docker run --env=POSTGRES_PASSWORD=pass -p 5432:5432 -d postgres
```

Log in to the postgres server. For example, using `psql` run:

```bash
psql -U postgres -h localhost -p 5432 -d postgres
```

In the `psql` terminal, create a database for tfoapi

```sql
CREATE DATABASE tfoapi;
```

### Terraform Custom Resource Definition

Install the Terraform CRD that will match the one that will be created in the vcluster. In general, the lastest if preferred:

```bash
kubectl apply -f https://raw.githubusercontent.com/GalleyBytes/terraform-operator/refs/heads/master/deploy/crds/tf.galleybytes.com_terraforms_crd.yaml
```



## API Server

Export environment variables for the API server:

```bash
export KUBECONFIG=~/.kube/config
export ADMIN_USERNAME=user
export ADMIN_PASSWORD='$2a$12$K.Hqh3sdBkdsnrA5zDuJLeMEoNclejP9UZjzxE6KpmsjQ4f01UdT.' # password
export TFO_API_VCLUSTER_DEBUG_HOST=127.0.0.1:8443
```

> :warning:  __Note on the env var `TFO_API_VCLUSTER_DEBUG_HOST`__ 
> When first starting the API server for the first time, there is no vcluster.  First start the `terraform-operator-remote-controller` Next, port-forward the vcluster that gets created.

__Start the API Server__

Start the API server. Use the databse url that matches how you set your development database.

```bash
go run cmd/main.go --db-url postgres://postgres:pass@localhost:5432/tfoapi
```



## Terraform Operator Remote Controller 

Export the environment variables that will allow the remote controller to connect to the server.

```bash
export KUBECONFIG=~/.kube/config
export CLIENT_NAME=docker-desktop
export TFO_API_PROTOCOL=http
export TFO_API_HOST=localhost
export TFO_API_PORT=3000
export TFO_API_LOGIN_USER=user
export TFO_API_LOGIN_PASSWORD=password
```

__Start the Remote Controller__

```bash
go run main.go
```

> :warning: Port Forward at this time
>
> The Remote Controller will fail "Setting Up" until the API server can check the status of the vcluster. A few seconds after started remote controller, the vcluster should be ready for port forwarding. 

Port forward the vcluster: 

```bash
CLIENT_NAME=docker-desktop
kubectl port-forward -n internal-$CLIENT_NAME svc/tfo-virtual-cluster 8443:8443
```

After running the port-forward command, the remote controller should be ready to go in a minute.

> The logs from the remote-controller should look something like this:
> ````
2025/05/02 15:40:49 Setting up
2025/05/02 15:40:49 VCluster is ready!
2025/05/02 15:40:49 TFO is ready!
2025/05/02 15:40:49 Starting informer
2025/05/02 15:40:49 Queue worker started


## Other Run Options

### Flags

You can run the binary with flags instead of environment variables: 

- `--addr`: Specify the address (e.g., `--addr ':3000'`)
- `--db-url`: Specify the database connection string (e.g., `--db-url postgres://user:password@localhost/dbname`)
