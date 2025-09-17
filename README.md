# Tyk Phantom Token Plugin

[![Quality](https://img.shields.io/badge/quality-demo-red)](https://curity.io/resources/code-examples/status/)
[![Availability](https://img.shields.io/badge/availability-source-blue)](https://curity.io/resources/code-examples/status/)

This repository contains a ready-to-run **Phantom Token** [Rich gRPC plugin](https://tyk.io/docs/api-management/plugins/rich-plugins/) for Tyk Gateway.  
The plugin is comprised of:
- A **plugin container** that implements the gRPC middleware
- A **bundle server** container to serve the signed plugin bundle to Tyk

> **Prerequisite:** You already have a running [Tyk Gateway](https://tyk.io/docs/tyk-self-managed/) environment and an instance of the Curity Identity Server configured to support [Introspection](https://curity.io/resources/learn/introspect-with-phantom-token/).


## Quick Start
```bash
git clone https://github.com/curityio/tyk-phantom-token-plugin.git
cd tyk-phantom-token-plugin
cp .env.example .env
# edit .env with your Curity introspection URL + creds

# Build & run the plugin + bundle server
docker compose up -d --build
```

## Plugin Bundle

Every release of this project includes a signed `phantom-bundle.zip` as an attached asset.  
Before starting the Docker Compose stack, download the bundle from the [GitHub Releases](../../releases) page.

After downloading, choose one of the following options:

### Bundle Server (default)
Place the file at `./bundles/phantom-bundle.zip` in this repo.  
Then start the stack with:  
```bash
docker compose up -d --build
```
The included bundle-server container will automatically serve the bundle to the Tyk Gateway.

### Self Hosted
Host the bundle on your own web server (e.g. S3, Nginx).
Update your Tyk Gateway configuration (`tyk.conf`) with the correct `bundle_base_url` pointing to that hosted location.

## Tyk Gateway Configuration
Enabling coprocess as global ENV’s in Tyk doesn’t work reliably (tested v5.8.3).  
Instead, configure these settings directly in `tyk.conf`.

Example:

```json
{
  "listen_port": 8080,
  "secret": "352d20ee67be67f6340b4c0605b044b7",

  "enable_coprocess": true,
  "coprocess_options": {
    "enable_coprocess": true,
    "coprocess_grpc_server": "tcp://phantom-plugin:50051"
  },

  "enable_bundle_downloader": true,
  "bundle_base_url": "http://bundle-server/",

  "use_db_app_configs": true,
  "db_app_conf_options": {
    "connection_string": "http://tyk-dashboard:3000",
    "node_is_segmented": false,
    "enable_app_key_hashing": false,
    "use_app_id_as_key": true
  },

  "storage": { "type": "redis", "host": "tyk-redis", "port": 6379 },
  "log_level": "debug"
}
```

> **Important**  
> - Set both the root `enable_coprocess` flag and the nested `coprocess_options.enable_coprocess`.  
> - If your gateway environment (`tyk.env`) has `TYK_GW_COPROCESSOPTIONS_COPROCESSGRPCSERVER=tcp://localhost:5555` or similar configured, remove or comment it out. It overrides these settings.  

## Networking
Ensure your existing Gateway can resolve and reach these services:

- `phantom-plugin:50051` (gRPC)
- `bundle-server` (HTTP 80)

If your ateway runs in Docker, connect this compose project to the same network:

```bash
# find gateway's network
docker inspect <your-tyk-gateway-container> --format '{{json .NetworkSettings.Networks}}' | jq

# attach the two services to that network
docker network connect <gw_network_name> tyk-phantom-token-phantom-plugin-1
docker network connect <gw_network_name> tyk-phantom-token-bundle-server-1
```

Or declare the external network directly in this repo’s `docker-compose.yml`:

```yml
networks:
  tyk:
    external: true
    name: [gw_network_name]
```

## API Configuration

Add to your API definition (OAS "View API designer” or raw YAML):

```yml
x-tyk-api-gateway:
  server:
    authentication:
      custom:
        enabled: true
  middleware:
    global:
      pluginConfig:
        driver: grpc
        bundle:
          enabled: true
          path: phantom-bundle.zip
```

## Test
Call an API configured with Custom Authentication Plugin:

```bash
curl -i -H "Authorization: Bearer OPAQUE_TOKEN" http://<your-gw-host>:8080/<your-api>/
```

The upstream request now contains `Authorization: Bearer <JWT>`.

## GitHub Actions and Releases

This repo includes two GitHub Actions workflows:

- **Build Plugin Image** – builds the gRPC plugin Docker image, publishes it as an artifact, and pushes to GHCR on release.  
- **Build Bundle ZIP** – generates a signed `phantom-bundle.zip` and publishes it as both an artifact and a GitHub Release asset.

## More Information

Please visit [curity.io](https://curity.io/) for more information about the Curity Identity Server.

Copyright (C) 2025 Curity AB.