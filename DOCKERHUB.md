<!-- Canonical source for the Docker Hub Overview. Pasted into the Hub page by hand
     (the description API rejects PATs). When you change this, re-paste it on Docker Hub. -->
# TraceGen

**One container that emits realistic, topology-rich OpenTelemetry traces, including AI agentic spans.** No microservices to deploy, no Docker Compose with 15 containers. A single 5.7 MB image simulates a full e-commerce platform: up to 28 services, 60 pods, 40 scenario flows, and 10 injectable failure modes, with full OTel GenAI semantic conventions for LLM/agent observability.

Built for testing observability platforms, load-testing trace pipelines, and showcasing distributed-system visualizations, for both traditional APM and LLM observability.

## Quick start

Point it at any OTLP/gRPC collector (Jaeger, Tempo, Grafana, an OTel Collector, or a commercial backend):

```bash
# Traces to a collector on your host
docker run --rm immersivefusion/tracegen -insecure -endpoint host.docker.internal:4317

# Crank up the volume and inject failures
docker run --rm immersivefusion/tracegen -insecure -endpoint host.docker.internal:4317 -level 8 -errors 5
```

The image is multi-arch (`linux/amd64`, `linux/arm64`), distroless, and runs as non-root.

## Three things to try

| Goal | Command |
| --- | --- |
| **AI agentic traces only** (LLM/GenAI spans) | `-ai-only` |
| **Traditional APM only** (no AI backends) | `-no-ai-backends` |
| **Chaos** (failures + unconsumed queues) | `-errors 10 -level 8 -no-consumers` |

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-endpoint` | `localhost:4317` | OTLP gRPC endpoint (`host:port`) |
| `-insecure` | `false` | Plaintext gRPC (no TLS) for local/in-cluster backends |
| `-headers` | | OTLP headers, `key=value` comma-separated (e.g. `api-key=SECRET`) |
| `-level` | `1` | Aggressiveness 1-10 (1 = whisper, 10 = SCREAM) |
| `-errors` | `0` | Error rate 0-10 (0 = none, 5 = normal, 10 = chaos) |
| `-complexity` | `normal` | Topology size: `light`, `normal`, `heavy` |
| `-ai-only` | `false` | Only run AI agentic scenarios |
| `-no-ai-backends` | `false` | Disable LLM/AI backends (AI spans emit errors) |
| `-no-consumers` | `false` | Publish to queues but never consume (backlog demo) |
| `-no-logs` | `false` | Traces only, no OTel log records |

## Why it exists

Existing trace generators are either flat span emitters (no service topology) or full demo apps that need Docker Compose and several GB of RAM, and none of them generate AI agentic traces. TraceGen produces topology-rich, failure-injectable traces from a single binary, covering both traditional microservice flows and AI agentic patterns. One image proves a platform can visualize both.

## Tags

- `latest`, `0.6.1`, ... track the GitHub releases. Pin by digest in production/cluster use.

## Source, issues, full docs

[github.com/ImmersiveFusion/if-opentelemetry-tracegen](https://github.com/ImmersiveFusion/if-opentelemetry-tracegen)

Apache-2.0. Built by Immersive Fusion.
