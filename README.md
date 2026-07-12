# llama.gw

llama.cpp inference gateway (daemon + proxy). Manages llama-server lifecycle with automatic model switching, health monitoring, and graceful restart.

## Architecture

```
  Hermes / OpenAI client
         │
         ▼
┌──────────────────────────┐
│  llmp   :9999            │  public endpoint
│  /v1/models  /v1/chat    │  parses model field, calls
│  → llmd/switch           │  llmd to switch, then
│                          │  reverse-proxies to backend
└────────┬─────────────────┘
         │  POST /switch
         ▼
┌──────────────────────────┐
│  llmd   :9898            │  control plane
│  /status  /switch        │  manages llama-server
│  30s heartbeat           │  lifecycle: kill old,
│  graceful shutdown       │  start new, health-check
└────────┬─────────────────┘
         │  start/kill + health check
         ▼
┌──────────────────────────┐
│  llama-server :9797      │  inference backend
│  llama.cpp GGUF models   │  one model at a time
└──────────────────────────┘
```

## Quick Start

```bash
# build
cd llama.gw
go build -o bin/llmd ./cmd/llmd
go build -o bin/llmp  ./cmd/llmp

# install systemd units (already configured)
systemctl --user daemon-reload
systemctl --user enable --now llmd
systemctl --user enable --now llmp
```

## API

### Proxy: llmp (:9999)

| Method | Path                     | Description                                           |
| ------ | ------------------------ | ----------------------------------------------------- |
| GET    | `/v1/models`           | List available models                                 |
| POST   | `/v1/chat/completions` | OpenAI-compatible chat (model switching is automatic) |

### Daemon: llmd (:9898)

| Method | Path        | Description                           |
| ------ | ----------- | ------------------------------------- |
| GET    | `/status` | Current model, backend health, uptime |
| POST   | `/switch` | Switch model:`{"model":"gemma26"}`  |

## Models (Samples)

| Alias       | GGUF                         | Size   |
| ----------- | ---------------------------- | ------ |
| `qwen35`    | Qwen3.6-35B-A3B-MXFP4_MOE    | 21GB   |
| `qwen27`    | Qwen3.6-27B-Q5_K_M           | 19GB   |
| `gemma26`   | gemma-4-26B-A4B-it-UD-Q4_K_M | 15.8GB |

Hermes config:

```yaml
providers:
  llamacpp:
    base_url: http://127.0.0.1:9999/v1
    default_model: Qwen3.6-35B-A3B-MXFP4_MOE
```

## systemd

Two units, independent lifecycle:

- **llmd.service** — manages llama-server child
- **llmp.service** — thin reverse proxy, Requires=llmd

## Project Structure

```
llama.gw/
├── cmd/
│   ├── llmd/main.go      # llmd: :9898
│   └── llmp/main.go      # llmp: :9999
├── internal/
│   └── config.go         # shared ports, models, fuzzy resolve
├── bin/
├── systemd/
│   ├── llmd.service
│   └── llmp.service
└── go.mod
```
