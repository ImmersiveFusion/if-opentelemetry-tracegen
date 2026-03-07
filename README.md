# OpenTelemetry Trace Generator

A single-binary distributed trace generator that produces realistic, topology-rich OTLP traces. No Docker, no microservices to deploy, no infrastructure - just one executable that simulates a full e-commerce platform with 20 services, 43 pods, and 15 scenario flows.

Built for testing observability platforms, load testing trace pipelines, and showcasing distributed system visualizations.

## Why This Exists

Every existing trace generator falls into one of two categories:

1. **Flat span generators** (telemetrygen, tracepusher) - produce uniform, identical spans with no service topology
2. **Full demo apps** (OTel Astronomy Shop, Jaeger HotROD) - require Docker Compose with 15+ containers and 8GB RAM

Nothing generates **topology-rich, failure-injectable traces from a single binary**. This tool fills that gap.

## Quick Start

```bash
# Download the latest release (or build from source)
go install github.com/ImmersiveFusion/if-opentelemetry-tracegen/cmd/tracegen@latest

# Run with your OTLP endpoint
tracegen -apikey YOUR_API_KEY -endpoint your-otlp-endpoint:443

# Or set the API key via environment variable
export OTEL_APIKEY=YOUR_API_KEY
tracegen -endpoint your-otlp-endpoint:443
```

### See It In Action

Try the [IAPM demo](https://demo.iapm.app) to see these traces rendered in an immersive 3D force-directed graph - no setup required.

## Features

### 20 Microservices

| Service | Pods | Role |
|---|---|---|
| web-frontend | 2 | Browser client, SPA |
| api-gateway | 3 | HTTP routing, auth |
| order-service | 3 | Order lifecycle |
| payment-service | 2 | Stripe integration |
| inventory-service | 2 | Stock management |
| notification-service | 2 | Event-driven notifications |
| user-service | 2 | Auth, profiles |
| cache-service | 3 | Redis cluster |
| search-service | 2 | Elasticsearch queries |
| scheduler-service | 1 | Cron jobs (singleton) |
| auth-service | 3 | JWT, webhook verification |
| recommendation-service | 2 | ML-based recommendations |
| cart-service | 2 | Shopping cart |
| product-service | 3 | Product catalog |
| shipping-service | 2 | Rates, labels, tracking |
| fraud-service | 2 | ML fraud scoring |
| email-service | 2 | SMTP relay (SendGrid) |
| tax-service | 1 | Tax calculation |
| analytics-service | 3 | Event tracking (Kafka) |
| config-service | 1 | Feature flags |

All 43 pods are distributed across 5 AKS VMSS nodes (2 node pools) with realistic `service.instance.id` and `host.name` resource attributes.

### 15 Scenario Flows

| Scenario | Graph Shape | Key Pattern |
|---|---|---|
| **Create Order** | Long chain (8 services, 14+ spans) | Producer/consumer with queue delays |
| **Search & Browse** | Linear with cache | Elasticsearch + Redis |
| **User Login** | Branching (success/failure) | Auth with session creation |
| **Failed Payment** | Error chain | Stripe 402 + error propagation |
| **Bulk Notifications** | Fan-out (3-5 parallel) | Batch email processing |
| **Health Check** | Star topology (6 parallel) | Concurrent health pings |
| **Inventory Sync** | Fan-out + reindex | Parallel cache warming |
| **Scheduled Report** | Headless chain (no UI) | Cron job entry point |
| **Stripe Webhook** | Headless chain (no gateway) | External callback entry |
| **Recommendations** | Scatter-gather / bowtie | Fan-out to 3, gather, cache |
| **Add to Cart** | Cross-service with feature flags | Config service + analytics |
| **Full Checkout** | Monster chain (16 services) | Tax+shipping parallel, fraud ML |
| **Shipping Update** | Carrier webhook (headless) | External webhook + email relay |
| **Saga Compensation** | V-shape (forward + 4-way reverse) | Payment retries + compensation fan-out |
| **Timeout Cascade** | Branching with circuit breaker | Stale cache fallback |

### Chaos & Failure Injection

| Feature | Description |
|---|---|
| **Lost messages** | 5% chance per queue hop that the consumer never fires - trace ends abruptly |
| **Dead consumer mode** | `-no-consumers` flag: producers fire, consumers never pick up. Messages pile up. |
| **Retry storms** | Payment retries 3x with exponential backoff before saga compensation |
| **Timeout cascades** | Search service times out, gateway returns 504, circuit breaker serves stale cache |
| **Saga compensation** | Payment fails after order+inventory committed - triggers 4-way parallel rollback |
| **Tunable error rate** | `-errors 0` (none) to `-errors 10` (chaos) with realistic .NET stack traces |

### Realistic Details

- **Stack traces**: Npgsql, StackExchange.Redis, Stripe SDK, Elasticsearch.Net, System.Net.Http
- **Database operations**: PostgreSQL INSERT/SELECT/UPDATE with semantic conventions
- **Cache operations**: Redis GET/SET/HSET/MSET/DEL with TTL and key attributes
- **Messaging**: RabbitMQ and Kafka with producer/consumer span kinds and queue delays
- **External APIs**: Stripe charges, SendGrid email, UPS shipping
- **ML inference**: Fraud detection model scoring with feature counts
- **Feature flags**: Config service checks that gate behavior

## Usage

```
tracegen [flags]

Flags:
  -apikey string     API key for OTLP endpoint (required, or set OTEL_APIKEY env var)
  -endpoint string   OTLP gRPC endpoint host:port (default "otlp.iapm.app:443")
  -level int         Aggressiveness 1-10 (default 1)
  -errors int        Error rate 0-10 (default 0)
  -no-consumers      Disable all async consumers
```

### Aggressiveness Levels

| Level | Label | Rate |
|---|---|---|
| 1 | whisper | ~2 traces/s |
| 2 | gentle | ~3 traces/s |
| 3 | calm | ~3 traces/s |
| 4 | moderate | ~5 traces/s |
| 5 | steady | ~7 traces/s |
| 6 | brisk | ~15 traces/s |
| 7 | aggressive | ~21 traces/s |
| 8 | intense | ~40 traces/s |
| 9 | firehose | ~83 traces/s |
| 10 | SCREAM | ~350 traces/s |

### Examples

```bash
# Gentle trace generation, no errors
tracegen -apikey $KEY -level 1

# Moderate load with normal error rates
tracegen -apikey $KEY -level 5 -errors 5

# Simulate dead consumers (messages pile up, consumers never fire)
tracegen -apikey $KEY -level 3 -no-consumers

# Chaos mode - maximum load and errors
tracegen -apikey $KEY -level 10 -errors 10

# Send to a local Jaeger/Tempo instance
tracegen -apikey $KEY -endpoint localhost:4317
```

## How It Compares

| Capability | tracegen | OTel telemetrygen | OTel Astronomy Shop | Jaeger HotROD | k6 + xk6-tracing |
|---|:---:|:---:|:---:|:---:|:---:|
| Single binary, zero infra | **5MB** | 1 binary | 15+ containers, 8GB | 4 containers | k6 + extension |
| Services | **20** | 1 | ~14 | 4 | User-defined |
| Pod instances | **43** | 0 | 1/svc | 0 | 0 |
| Scenario flows | **15** | 0 | ~5 | 1 | User-defined |
| Diamond dependencies | **Yes** | No | Implicit | No | No |
| Scatter-gather | **Yes** | No | No | No | No |
| Lost messages | **Yes** | No | No | No | No |
| Dead consumer mode | **Yes** | No | No | No | No |
| Saga compensation | **Yes** | No | No | No | No |
| Retry storms | **Yes** | No | No | No | No |
| Timeout cascade | **Yes** | No | No | No | No |
| Tunable error rate | **0-10** | No | Fixed | No | No |
| Tunable throughput | **2-350/s** | Rate flag | Locust | Fixed | k6 VUs |
| Non-UI entry points | **3** | No | No | No | No |
| Startup time | **<1s** | <1s | 3-5 min | 30s | <5s |

## Building From Source

```bash
git clone https://github.com/ImmersiveFusion/if-opentelemetry-tracegen.git
cd if-opentelemetry-tracegen
go build -o tracegen ./cmd/tracegen
```

### Cross-compile

```bash
# Linux
GOOS=linux GOARCH=amd64 go build -o tracegen ./cmd/tracegen

# macOS (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -o tracegen ./cmd/tracegen

# Windows
GOOS=windows GOARCH=amd64 go build -o tracegen.exe ./cmd/tracegen
```

## Compatible Backends

Works with any OTLP gRPC-compatible backend:

- [IAPM](https://iapm.app) (Immersive APM)
- Jaeger
- Grafana Tempo
- Honeycomb
- New Relic
- Datadog (with OTLP endpoint)
- Splunk Observability
- Elastic APM
- Any OpenTelemetry Collector

## License

Apache License 2.0 - see [LICENSE](LICENSE) for details.

Copyright 2026 [ImmersiveFusion, Inc.](https://immersivefusion.com)
