# Tyk Phantom Token Plugin

[![Quality](https://img.shields.io/badge/quality-demo-red)](https://curity.io/resources/code-examples/status/)
[![Availability](https://img.shields.io/badge/availability-source-blue)](https://curity.io/resources/code-examples/status/)

This repository contains a ready-to-run **Phantom Token** gRPC plugin for Tyk Gateway.  
It comes with:
- A **plugin container** that implements the gRPC middleware
- A **bundle server** container to serve the signed plugin bundle to Tyk
- GitHub Actions workflows to build the plugin image and the bundle ZIP automatically

> **Prerequisite:** You already have a running Tyk Gateway environment.


## Quick Start
```bash
git clone https://github.com/curity/tyk-phantom-token.git
cd tyk-phantom-token
cp .env.example .env
# edit .env with your Curity introspection URL + creds

# Build & run only the plugin + bundle server
docker compose up -d --build
```

## Wire it into your existing Tyk Gateway
NOTE: Enabling coprocess as global ENV's in Tyk doesn't seem to work (v5.8.3) but configuring these parameters in tyk.conf does.

Here's a minimal example of a tyk.conf:
NOTE “Set both root and nested flags to enable CP in tyk.conf
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

“If your gateway env has TYK_GW_COPROCESSOPTIONS_COPROCESSGRPCSERVER=tcp://localhost:5555, remove or comment it, or it will override the correct Docker hostname.”

“Ensure your plugin stack shares the same Docker network as your gateway; set networks.tyk.external: true and name: <your network> in docker-compose.yml.”

API config (OAS “View API designer” or raw):

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

## Networking
Make sure your existing Gateway can reach these two services by name:

    phantom-plugin:50051 (gRPC)

    bundle-server (HTTP 80)

If your Gateway runs in Docker, connect this compose project to the same network:

```bash
# find gateway's network
docker inspect <your-tyk-gateway-container> --format '{{json .NetworkSettings.Networks}}' | jq

# attach the two services to that network
docker network connect <gw_network_name> tyk-phantom-token-phantom-plugin-1
docker network connect <gw_network_name> tyk-phantom-token-bundle-server-1
```

Or, define the network in this repo’s docker-compose.yml:

```yml
networks:
  tyk:
    external: true
    name: [gw_network_name]
```

## Test

Tail plugin logs:

```bash
docker compose logs -f phantom-plugin
```

Call an API configured with Custom auth + the above bundle:

```bash
curl -i -H "Authorization: Bearer OPAQUE_OPAQUE" http://<your-gw-host>:8080/<your-api>/anything
```

Expect your plugin logs to show PhantomAuthCheck and the upstream to have Authorization: Bearer <JWT>.