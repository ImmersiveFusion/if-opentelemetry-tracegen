# Scenario Expansion Analysis: OpenTelemetry Trace Generator (15 to 30+ Flows)

**PS ID:** ps-tracegen-001
**Entry ID:** e-001
**Analysis Type:** gap + trade-off + impact
**Date:** 2026-03-07
**Analyst:** ps-analyst v2.3.0

---

## L0: Executive Summary (ELI5)

The trace generator currently simulates 20 microservices and 15 different "stories" (scenarios) that show how a real e-commerce platform behaves — orders being placed, payments failing, searches timing out. It does this from a single binary with no infrastructure.

The goal is to grow from 15 to 30+ scenarios by adding both new traditional e-commerce stories AND scenarios for modern AI-powered features like chatbots, semantic search, and autonomous agents.

**What we found:** The current code is 2,347 lines, with 15 scenarios averaging 129 lines each. Adding 15+ new scenarios will grow the file to approximately 5,500-6,500 lines, which is the main structural risk. The file should be split into per-category files before reaching that size, or the codebase will become difficult to maintain.

**What to build:** We recommend 13 new traditional scenarios and 12 new AI/agentic scenarios (25 new total, reaching 40 flows). The AI scenarios require 8 new services (primarily for LLM, vector search, and content moderation infrastructure) and introduce observability patterns that do not exist in the current code — specifically multi-turn agent loops, token budget tracking, embedding pipeline spans, and safety filter events.

**What to do first:** Split the file into packages, then implement AI scenarios in priority order: RAG Search, AI Chatbot with Tool Use, and AI Content Moderation — these three scenarios alone introduce every new AI observability pattern and justify the new service additions.

---

## L1: Technical Analysis (Software Engineer)

### Current State Baseline

Evidence from `cmd/tracegen/main.go` (2,347 lines, 1 package, 1 file):

| Metric | Value | Source |
|--------|-------|--------|
| Total lines | 2,347 | `wc -l` |
| Scenario functions | 15 | `grep "^func"` |
| Span creation calls | 138 | `grep "tracer("` |
| Avg lines/scenario | 129 | (1,935 scenario lines / 15) |
| Infrastructure lines | 413 | Lines 1-261 + 2197-2347 |
| Services (tracerPool keys) | 20 | Pod list, lines 132-196 |
| Pod instances | 43 | Pod list count |
| AKS nodes | 5 | Node names in pod list |

Scenario line counts (evidence: function boundary analysis):
- Smallest: `healthCheckFlow` = 61 lines (fan-out, no queues)
- Median: `recommendationFlow` = 124 lines (scatter-gather)
- Largest: `fullCheckoutFlow` = 310 lines (monster chain, 16 services)
- `sagaCompensationFlow` = 244 lines (compensation fan-out)

**Key patterns already implemented** (evidence from code):
- Linear chains (search, login)
- Fan-out/fan-in (health check, recommendations scatter-gather)
- Producer/consumer with queue delays and `consumersEnabled` guard
- Error injection via `errorChance(baseRate)` scaling with `errorMultiplier`
- Exception recording with realistic .NET stack traces
- Concurrent goroutines with channel synchronization
- Multi-attempt retry loops (saga: 3 stripe retries with backoff)
- Headless entry points (no UI/gateway root — stripeWebhookFlow, shippingUpdateFlow, scheduledReportFlow)
- Circuit breaker + stale cache fallback (timeoutCascadeFlow)
- ML inference span with `ml.model`, `ml.features`, `ml.inference_ms` attributes (fullCheckoutFlow line 1578-1587)

### Gap: Patterns Not Yet Present in Any Scenario

| Pattern | Why It Matters for Observability |
|---------|----------------------------------|
| LLM token accounting (`gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`) | Core AI cost/performance signal |
| Embedding generation pipeline | Shows vector-DB-bound latency |
| Vector similarity search span | Distinct from Elasticsearch text search |
| Multi-turn agent loop (plan -> tool -> reflect) | Creates self-similar subtrace topology |
| Safety filter / content moderation gate | Error mode unique to AI |
| Streaming response (SSE/chunked) | Not representable as single HTTP span |
| Model fallback chain (primary LLM -> smaller model -> cached) | Cascading failure pattern for AI |
| Rate limit / quota exhaustion at LLM API | Different from HTTP 429 in semantics |
| Batch embedding job | High parallelism, large fan-out |
| Human-in-the-loop escalation | Async handoff to external system |

---

### Part 1: New Traditional E-Commerce Scenarios

#### T-01: User Registration with Email Verification

- **Services:** web-frontend, api-gateway, auth-service, user-service, cache-service, email-service, notification-service
- **Graph shape:** Linear chain with async email tail (7 services, ~12 spans)
- **Span count estimate:** 12-14
- **Key patterns:** Duplicate-detection DB check, Redis SET for verification token with TTL (3600s), async email dispatch via notification queue
- **New error modes:** Duplicate email (409), expired verification token
- **Observability value:** Shows registration funnel bottleneck — auth-service DB lookup is critical path

#### T-02: Product Review Submission with Moderation Gate

- **Services:** web-frontend, api-gateway, auth-service, product-service, analytics-service, notification-service
- **Graph shape:** Linear with conditional async notification (6 services, ~10 spans)
- **Span count estimate:** 10-12
- **Key patterns:** Optimistic write + async moderation queue; 15% chance review is held for manual review (notification to admin)
- **Attributes:** `review.rating`, `review.word_count`, `review.moderation_status`
- **Observability value:** Shows content write path vs. read path asymmetry

#### T-03: Return and Refund Initiation

- **Services:** web-frontend, api-gateway, auth-service, order-service, payment-service, inventory-service, notification-service, email-service
- **Graph shape:** Long linear chain with parallel refund+restock (8 services, ~16 spans)
- **Span count estimate:** 16-18
- **Key patterns:** Order state machine transition (shipped -> return_requested -> refund_pending), parallel Stripe refund + inventory restock
- **Attributes:** `return.reason`, `refund.amount`, `return.tracking_number`
- **Error modes:** Refund window expired, already refunded
- **Observability value:** Reverse money flow — Stripe refund latency visible vs. internal stock update

#### T-04: Wishlist Add and Price Drop Alert

- **Services:** web-frontend, api-gateway, auth-service, product-service, cache-service, analytics-service
- **Graph shape:** Short linear + async background job (5 services synchronous, ~8 spans)
- **Span count estimate:** 8-10
- **Key patterns:** Write-through cache for wishlist (Redis SADD), async price monitoring via scheduler
- **Attributes:** `wishlist.user_id`, `wishlist.product_id`, `wishlist.size`
- **Observability value:** Simple write-path scenario; high-frequency operations stress cache

#### T-05: Coupon / Discount Code Application

- **Services:** web-frontend, api-gateway, auth-service, cart-service, product-service, cache-service, tax-service
- **Graph shape:** Linear with validation branch (7 services, ~11 spans)
- **Span count estimate:** 11-13
- **Key patterns:** Coupon validation DB lookup, cart recalculation chain, tax recalculation; 20% coupon invalid/expired
- **Attributes:** `coupon.code`, `coupon.discount_percent`, `coupon.type` (flat/percent/free_shipping)
- **Error modes:** Expired coupon, minimum spend not met, already used
- **Observability value:** Shows cart recalculation ripple — every validation call adds latency

#### T-06: Gift Card Purchase and Redemption

- **Services:** web-frontend, api-gateway, auth-service, payment-service, user-service, email-service, order-service
- **Graph shape:** Two separate flows — purchase (linear, ~8 spans) and redemption (linear split, ~10 spans)
- **Span count estimate:** 10-12 per flow
- **Key patterns:** Gift card code generation (UUID stored in DB), balance check + partial payment split (gift card + credit card)
- **Attributes:** `gift_card.code`, `gift_card.balance`, `gift_card.applied_amount`
- **Error modes:** Zero balance, expired, invalid code
- **Observability value:** Payment splitting — two Stripe calls in sequence (gift card partial, then credit card remainder)

#### T-07: Subscription Plan Management

- **Services:** web-frontend, api-gateway, auth-service, user-service, payment-service, scheduler-service, notification-service
- **Graph shape:** Linear setup + recurring cron trigger (5 services sync, scheduler headless trigger)
- **Span count estimate:** 12-15 (setup) + 8 (renewal cron)
- **Key patterns:** Stripe subscription object (not one-time charge), renewal webhook entering at payment-service
- **Attributes:** `subscription.plan`, `subscription.interval`, `subscription.next_billing_date`
- **Error modes:** Card expired at renewal, downgrade grace period
- **Observability value:** Long-running subscription traces show billing reliability over time

#### T-08: A/B Test Exposure and Conversion Tracking

- **Services:** web-frontend, api-gateway, config-service, analytics-service, cache-service
- **Graph shape:** Star from api-gateway (3 parallel calls) + async analytics
- **Span count estimate:** 8-10
- **Key patterns:** Feature flag variant assignment (config-service), sticky session (cache), conversion event (analytics Kafka)
- **Attributes:** `experiment.id`, `experiment.variant`, `experiment.user_bucket`
- **Observability value:** Shows config-service as critical path for every page load; config latency = experiment quality

#### T-09: Rate Limit and Throttling Enforcement

- **Services:** web-frontend, api-gateway, cache-service
- **Graph shape:** Short (3 services, 4-5 spans) — early termination pattern
- **Span count estimate:** 4-6
- **Key patterns:** Redis sliding window counter (INCR + EXPIRE), 429 response with Retry-After header, analytics event for rate limit hit
- **Attributes:** `ratelimit.limit`, `ratelimit.remaining`, `ratelimit.reset_at`
- **Error modes:** Burst limit exceeded, quota exhausted
- **Observability value:** Short high-frequency traces that cluster in dashboards; useful for testing ingestion at high span/s

#### T-10: Admin Product CRUD Operations

- **Services:** web-frontend, api-gateway, auth-service, product-service, cache-service, search-service, analytics-service
- **Graph shape:** Linear write + parallel cache invalidation + search reindex (7 services, ~14 spans)
- **Span count estimate:** 14-16
- **Key patterns:** Admin JWT with elevated claims, write-through invalidation (Redis DEL + ES reindex), optimistic lock on product version
- **Attributes:** `admin.user_id`, `product.sku`, `product.version`
- **Observability value:** Shows write-amplification: one product update triggers cache + search index

#### T-11: Order History Pagination

- **Services:** web-frontend, api-gateway, auth-service, order-service, cache-service
- **Graph shape:** Linear (5 services, ~8 spans)
- **Span count estimate:** 8-10
- **Key patterns:** Keyset pagination (no OFFSET), Redis GET with page cursor, DB SELECT with composite index hint
- **Attributes:** `pagination.cursor`, `pagination.page_size`, `db.rows_returned`
- **Observability value:** Shows pagination query performance; useful for detecting N+1 problems

#### T-12: Customer Support Ticket Creation

- **Services:** web-frontend, api-gateway, auth-service, order-service, notification-service, email-service, analytics-service
- **Graph shape:** Linear with async email tail (6 services, ~10 spans)
- **Span count estimate:** 10-12
- **Key patterns:** Ticket number generation, order lookup (cross-service context), SLA assignment via config-service
- **Attributes:** `ticket.id`, `ticket.category`, `ticket.priority`, `ticket.sla_hours`
- **Observability value:** Cross-domain trace — support tickets reference order IDs

#### T-13: Multi-Currency Price Display

- **Services:** web-frontend, api-gateway, product-service, cache-service, tax-service
- **Graph shape:** Linear with parallel FX lookup + tax calc (4 services, ~9 spans)
- **Span count estimate:** 9-11
- **Key patterns:** External FX rate API call (peer.service = "fixer-api"), Redis cache for rates (TTL 900s), currency rounding rules
- **Attributes:** `currency.from`, `currency.to`, `currency.rate`, `currency.rate_source`
- **Observability value:** External FX API latency vs. cached rate hit ratio visible in trace

---

### Part 2: AI Agentic Scenarios

These scenarios require new services. All use the OTel Semantic Conventions for Generative AI (`gen_ai.*` namespace, published as semconv experimental in v1.27+).

#### New Services Required

| Service | Pods | Node Distribution | Role |
|---------|------|-------------------|------|
| llm-gateway | 3 | vmss000000, vmss000001, vmss000002 | LLM API proxy: rate limiting, key rotation, model routing, token accounting |
| embedding-service | 2 | vmss000000, vmss000001 | Text-to-vector embedding (OpenAI text-embedding-3-small or local model) |
| vector-db-service | 2 | vmss000001, vmss000002 | Qdrant/Weaviate wrapper: upsert, query, delete |
| ai-agent-service | 2 | vmss000000, vmss000002 | Multi-step agent orchestration: plan, tool call, reflect |
| content-moderation-service | 2 | vmss000001, vmss000000 | LLM-based content safety scoring, flagging, human review queue |
| model-registry-service | 1 | vmss000000 | Model version management, A/B routing, canary deployment |
| feature-store-service | 2 | vmss000001, vmss000002 | Feature retrieval for ML inference (online feature serving) |
| data-pipeline-service | 2 | vmss000002, vmss000000 | Batch ETL for ML training data preparation |

**New pod total:** 16 new pods. Combined with existing 43 = **59 pods** across 5 nodes.

**Note on llm-gateway vs. direct LLM calls:** Every AI scenario routes through `llm-gateway` rather than calling `api.openai.com` directly. This is architecturally realistic (token budget enforcement, key rotation, audit logging, fallback model routing) and observability-correct (the gateway emits the `gen_ai.*` spans, not individual services).

---

#### AI-01: Semantic Search with RAG (Retrieval-Augmented Generation)

**Description:** User enters a natural-language search query. The system converts it to an embedding vector, retrieves the top-K semantically similar products from the vector database, then passes results to an LLM for reranking and natural-language summary generation. The final response is a ranked product list with an AI-generated explanation.

**Services involved:** web-frontend, api-gateway, auth-service, embedding-service, vector-db-service, llm-gateway, cache-service, search-service (fallback), analytics-service

**Trace topology (graph shape):**
```
web-frontend
  -> api-gateway
     -> auth-service (validate token)
     -> embedding-service (generate query embedding)
        -> llm-gateway [embed] (POST /v1/embeddings)
     -> vector-db-service (similarity search)
     -> llm-gateway [rerank] (POST /v1/chat/completions — reranking prompt)
     -> cache-service (SET result, TTL 300s)
     -> analytics-service (TrackEvent semantic_search.complete)
```
Shape: Linear backbone with two sequential LLM calls

**Span count estimate:** 14-16

**Key spans with realistic attributes:**

```
embedding-service / "GenerateQueryEmbedding"
  gen_ai.system = "openai"
  gen_ai.request.model = "text-embedding-3-small"
  gen_ai.usage.input_tokens = 12-48 (query token count)
  gen_ai.response.model = "text-embedding-3-small"
  embedding.dimensions = 1536
  embedding.latency_ms = 80-250

vector-db-service / "VectorSearch"
  db.system = "qdrant"
  db.operation = "search"
  vector_db.collection = "product-embeddings"
  vector_db.top_k = 20
  vector_db.returned = 18
  vector_db.similarity_metric = "cosine"
  vector_db.search_latency_ms = 15-60

llm-gateway / "POST /v1/chat/completions (rerank)"
  gen_ai.system = "openai"
  gen_ai.request.model = "gpt-4o-mini"
  gen_ai.usage.input_tokens = 800-2400
  gen_ai.usage.output_tokens = 150-400
  gen_ai.request.max_tokens = 512
  gen_ai.response.finish_reason = "stop"
  llm.latency_ms = 400-1800
```

**Error modes specific to AI:**
- `embedding-service` returns 429 (OpenAI rate limit) -> fallback to Elasticsearch text search
- `vector-db-service` timeout -> serve Elasticsearch results without reranking
- LLM response exceeds `max_tokens` -> `finish_reason = "length"`, truncated response logged as warning span event

**What makes it interesting for observability:** The branching fallback chain (vector search + LLM fails -> Elasticsearch text search) creates two distinct trace shapes from the same entry point. Token counts on every LLM span make cost attribution visible. The embedding latency bottleneck becomes obvious in a waterfall view.

---

#### AI-02: AI Chatbot with Tool Use (Conversational Commerce)

**Description:** Customer asks "Where is my order?" or "Find me a laptop under $800." The AI agent receives the query, plans which tools to call (order lookup API, product search API, shipping API), executes them in sequence or parallel, and synthesizes a response. This is a realistic LLM tool-use / function-calling pattern.

**Services involved:** web-frontend, api-gateway, auth-service, ai-agent-service, llm-gateway, order-service, search-service, shipping-service, cache-service

**Trace topology:**
```
web-frontend (chat message submission)
  -> api-gateway
     -> auth-service
     -> ai-agent-service / "ProcessChatMessage"
        -> llm-gateway / "PlanToolUse" (determines which tools to call)
        -> [parallel tool execution]
           -> order-service / "GetOrderStatus"
           -> search-service / "ProductSearch"
           -> shipping-service / "GetTrackingInfo"
        -> llm-gateway / "SynthesizeResponse" (tools results -> natural language)
        -> cache-service / SET conversation context (TTL 1800s)
```
Shape: Plan -> parallel fan-out -> gather -> synthesize (double bowtie)

**Span count estimate:** 18-22

**Key spans with realistic attributes:**

```
ai-agent-service / "ProcessChatMessage"
  agent.session_id = "sess_a7b3c..."
  agent.turn_number = 1-5 (multi-turn)
  agent.intent = "order_status" | "product_search" | "general"

llm-gateway / "PlanToolUse"
  gen_ai.system = "openai"
  gen_ai.request.model = "gpt-4o"
  gen_ai.usage.input_tokens = 200-800
  gen_ai.usage.output_tokens = 50-200
  agent.tools_available = 8
  agent.tools_selected = ["get_order", "get_tracking"]
  gen_ai.response.finish_reason = "tool_calls"

llm-gateway / "SynthesizeResponse"
  gen_ai.system = "openai"
  gen_ai.request.model = "gpt-4o"
  gen_ai.usage.input_tokens = 400-1600
  gen_ai.usage.output_tokens = 100-400
  gen_ai.response.finish_reason = "stop"
```

**Error modes specific to AI:**
- `llm-gateway` returns tool_call with unknown tool name -> agent logs `agent.error = "hallucinated_tool"`, falls back to generic response
- Token budget exceeded mid-plan -> `agent.error = "token_budget_exceeded"`, truncated context window strategy kicks in
- Tool result exceeds context limit -> summarization sub-call to LLM before synthesis

**What makes it interesting for observability:** The agent loop creates a self-similar trace topology. The plan->tool->synthesize pattern repeats for each conversation turn. Watching token consumption across turns reveals context window pressure. The hallucinated tool error mode is unique to AI.

---

#### AI-03: AI Content Generation (Product Description)

**Description:** Admin triggers AI generation of a product description. The system fetches product attributes, calls the LLM to generate marketing copy, scores it for brand safety via content moderation, and writes it back to the product DB. Optionally publishes A/B variant.

**Services involved:** web-frontend, api-gateway, auth-service, product-service, llm-gateway, content-moderation-service, cache-service, analytics-service

**Trace topology:**
```
web-frontend (admin action)
  -> api-gateway
     -> auth-service
     -> product-service / "GetProductAttributes"
     -> llm-gateway / "GenerateProductDescription"
     -> content-moderation-service / "ScoreContent"
        -> llm-gateway / "ModerationCheck" (if score borderline)
     -> product-service / "UPDATE product SET description"
     -> cache-service / DEL product cache
     -> analytics-service / TrackEvent content.generated
```
Shape: Linear with conditional moderation branch

**Span count estimate:** 12-15

**Key spans:**
```
llm-gateway / "GenerateProductDescription"
  gen_ai.request.model = "gpt-4o"
  gen_ai.usage.input_tokens = 300-600
  gen_ai.usage.output_tokens = 200-800
  gen_ai.request.temperature = 0.7
  content.type = "product_description"

content-moderation-service / "ScoreContent"
  moderation.score = 0.0-1.0
  moderation.categories = ["hate", "violence", "spam"]
  moderation.action = "approve" | "flag" | "block"
  moderation.model = "openai-moderation-latest"
```

**Error modes:** Safety filter triggered (action = "block"), LLM generates off-topic content (moderation score > 0.7 causes retry)

---

#### AI-04: Dynamic AI Pricing Agent

**Description:** A scheduled agent monitors competitor prices, analyzes demand signals, and adjusts product prices autonomously. This is a headless scenario (scheduler-service entry point) with external web scraping calls.

**Services involved:** scheduler-service, ai-agent-service, llm-gateway, product-service, analytics-service, cache-service, feature-store-service

**Trace topology:**
```
scheduler-service / "CronJob: PricingAgent" (headless)
  -> feature-store-service / "GetDemandFeatures"
  -> llm-gateway / "AnalyzeCompetitorData"
  -> ai-agent-service / "ComputePriceRecommendations"
     -> llm-gateway / "PriceOptimizationReasoning"
     -> product-service / "BatchUpdatePrices"
        -> [parallel] UPDATE price for N products
  -> analytics-service / TrackEvent pricing.adjusted
```
Shape: Headless linear with parallel batch write

**Span count estimate:** 14-18

**Key attributes:**
```
ai-agent-service / "ComputePriceRecommendations"
  agent.products_analyzed = 50-500
  agent.price_changes = 5-50
  agent.avg_change_percent = -5% to +8%
  agent.strategy = "competitive" | "demand_based" | "margin_floor"

feature-store-service / "GetDemandFeatures"
  feature_store.features = ["page_views_7d", "cart_adds_7d", "purchase_rate"]
  feature_store.entity = "product"
  feature_store.latency_ms = 10-40
```

**Error modes:** Competitor scraping blocked (403) -> agent uses cached data; LLM reasoning produces price below cost floor -> guardrail blocks update

---

#### AI-05: Autonomous Inventory Reordering Agent

**Description:** An agent monitors stock levels, calls demand forecasting, decides reorder quantities, and creates purchase orders — all without human approval below a threshold.

**Services involved:** scheduler-service, ai-agent-service, llm-gateway, inventory-service, order-service, notification-service, feature-store-service

**Trace topology:**
```
scheduler-service / "CronJob: InventoryAgent" (headless)
  -> inventory-service / "GetLowStockItems"
  -> feature-store-service / "GetDemandForecastFeatures"
  -> ai-agent-service / "ForecastDemand"
     -> llm-gateway / "DemandReasoningPrompt"
  -> [parallel for each low-stock item]
     -> inventory-service / "CreatePurchaseOrder"
     -> notification-service / "AlertProcurement"
```
Shape: Headless scatter-gather with agent reasoning

**Span count estimate:** 16-20

**Key attributes:**
```
ai-agent-service / "ForecastDemand"
  agent.items_below_threshold = 3-15
  agent.forecast_horizon_days = 30
  agent.confidence = 0.72-0.95

llm-gateway / "DemandReasoningPrompt"
  gen_ai.request.model = "gpt-4o-mini"
  gen_ai.usage.input_tokens = 500-1500
  gen_ai.usage.output_tokens = 100-300
  agent.reasoning_type = "demand_forecast"
```

---

#### AI-06: AI Customer Support Agent with Escalation

**Description:** A support ticket arrives. The AI classifies sentiment and intent, attempts auto-response, and escalates to human if confidence is low or sentiment is highly negative.

**Services involved:** web-frontend, api-gateway, auth-service, ai-agent-service, llm-gateway, content-moderation-service, notification-service, order-service, email-service

**Trace topology:**
```
web-frontend (ticket form submission)
  -> api-gateway
     -> auth-service
     -> ai-agent-service / "ClassifyTicket"
        -> llm-gateway / "SentimentClassification"
        -> llm-gateway / "IntentClassification"
        -> order-service / "GetCustomerContext" (recent orders)
        -> llm-gateway / "GenerateAutoResponse" (if confidence > 0.8)
        -> content-moderation-service / "ModerationCheck"
     -> [branch: escalate OR auto-respond]
        -> notification-service / "NotifyHumanAgent" (escalation path)
        -> email-service / "SendAutoResponse" (auto-respond path)
```
Shape: Linear with conditional branch at end

**Span count estimate:** 16-20

**Key attributes:**
```
llm-gateway / "SentimentClassification"
  gen_ai.request.model = "gpt-4o-mini"
  agent.sentiment = "positive" | "neutral" | "negative" | "hostile"
  agent.sentiment_score = -1.0 to 1.0
  agent.confidence = 0.6-0.99

ai-agent-service / "ClassifyTicket"
  agent.auto_resolve = true | false
  agent.escalation_reason = "low_confidence" | "hostile_sentiment" | "complex_refund"
  ticket.category = "shipping" | "payment" | "product" | "returns"
```

**Error modes:** LLM classification confidence below threshold -> escalation; auto-response moderation blocked; LLM timeout -> immediate human escalation

---

#### AI-07: Embedding Generation Pipeline (Bulk Product Indexing)

**Description:** A scheduled batch job processes all product descriptions through the embedding service and upserts vectors into the vector DB. Large fan-out pattern.

**Services involved:** scheduler-service, data-pipeline-service, embedding-service, llm-gateway (embeddings API), vector-db-service, cache-service, analytics-service

**Trace topology:**
```
scheduler-service / "CronJob: EmbeddingPipeline" (headless)
  -> data-pipeline-service / "FetchProductBatch"
  -> [parallel batch chunks, each]:
     -> embedding-service / "BatchEmbed" (N products per chunk)
        -> llm-gateway / "POST /v1/embeddings (batch)"
     -> vector-db-service / "BatchUpsert"
  -> analytics-service / "TrackEvent embedding.pipeline.complete"
```
Shape: Headless with high-parallelism fan-out (5-10 concurrent chunks)

**Span count estimate:** 25-40 (fan-out makes this one of the largest traces)

**Key attributes:**
```
embedding-service / "BatchEmbed"
  gen_ai.request.model = "text-embedding-3-small"
  gen_ai.usage.input_tokens = 2000-8000 (batch)
  embedding.batch_size = 50-100
  embedding.dimensions = 1536
  embedding.throughput_per_second = 500-2000 tokens/s

vector-db-service / "BatchUpsert"
  db.system = "qdrant"
  db.operation = "upsert"
  vector_db.collection = "product-embeddings"
  vector_db.vectors_upserted = 50-100
  vector_db.latency_ms = 20-80
```

**Error modes:** Embedding API quota exceeded mid-batch -> checkpoint and retry; vector DB write conflict during concurrent upsert; batch too large (token limit exceeded) -> chunk splitting

**What makes it interesting for observability:** The fan-out creates a wide trace. Token consumption per batch and total pipeline cost is visible. Throughput (vectors/second) vs. API rate limit is a natural alerting signal.

---

#### AI-08: AI-Powered Fraud Detection with Explainability

**Description:** Enhanced version of the existing fraud check. Instead of a static ML model score, an LLM generates a natural-language explanation of the fraud decision, including which features most influenced the score (SHAP-inspired).

**Services involved:** fraud-service, llm-gateway, feature-store-service, analytics-service

**Trace topology:**
```
[called from within fullCheckoutFlow or as standalone]
fraud-service / "AnalyzeTransactionWithExplanation"
  -> feature-store-service / "GetTransactionFeatures"
  -> fraud-service / "ML ScoreTransaction" (existing model)
  -> llm-gateway / "GenerateFraudExplanation"
     (input: feature vector + score -> output: human-readable explanation)
  -> analytics-service / "TrackEvent fraud.explanation.generated"
```
Shape: Sequential chain with LLM at end

**Span count estimate:** 10-12

**Key attributes:**
```
llm-gateway / "GenerateFraudExplanation"
  gen_ai.request.model = "gpt-4o-mini"
  gen_ai.usage.input_tokens = 400-800
  gen_ai.usage.output_tokens = 100-300
  fraud.explanation_requested = true
  fraud.top_features = ["ip_velocity", "card_age", "shipping_mismatch"]
  fraud.score = 0.1-0.9

feature-store-service / "GetTransactionFeatures"
  feature_store.features_requested = 42
  feature_store.features_returned = 42
  feature_store.latency_ms = 5-25
```

---

#### AI-09: Recommendation Model Retraining Pipeline

**Description:** A weekly scheduled job that pulls user interaction data, runs feature extraction, fine-tunes the recommendation model, evaluates it, and deploys to the model registry if it passes quality gates.

**Services involved:** scheduler-service, data-pipeline-service, analytics-service, feature-store-service, model-registry-service, recommendation-service, notification-service

**Trace topology:**
```
scheduler-service / "CronJob: ModelRetraining" (headless)
  -> data-pipeline-service / "ExtractTrainingData"
     -> analytics-service / "QueryInteractionEvents"
  -> feature-store-service / "PrepareFeatures"
  -> data-pipeline-service / "TrainModel"
     [long span, 30-120s simulated with big sleep range]
  -> model-registry-service / "RegisterModelVersion"
  -> model-registry-service / "EvaluateModel" (offline metrics)
  -> [branch: deploy or reject]
     -> recommendation-service / "UpdateModelEndpoint" (deploy path)
     -> notification-service / "AlertMLTeam" (both paths)
```
Shape: Headless sequential pipeline with conditional deploy branch

**Span count estimate:** 14-18

**Key attributes:**
```
data-pipeline-service / "TrainModel"
  ml.framework = "pytorch"
  ml.training_samples = 50000-500000
  ml.epochs = 5-20
  ml.loss_final = 0.15-0.45
  ml.training_duration_ms = 30000-120000

model-registry-service / "EvaluateModel"
  ml.metric.ndcg_at_10 = 0.35-0.65
  ml.metric.hit_rate_at_5 = 0.40-0.70
  ml.baseline_ndcg = 0.50
  ml.decision = "deploy" | "reject"
```

**Error modes:** Training loss divergence (NaN), evaluation below baseline (reject), model registry upload timeout

---

#### AI-10: AI Content Moderation Pipeline (UGC Reviews)

**Description:** When a user submits a product review or listing image, the content moderation service scores it using LLM-based classifiers, flags borderline content for human review, and blocks policy violations.

**Services involved:** web-frontend, api-gateway, auth-service, content-moderation-service, llm-gateway, notification-service, analytics-service

**Trace topology:**
```
web-frontend (review submission)
  -> api-gateway
     -> auth-service
     -> content-moderation-service / "ModerateContent"
        -> llm-gateway / "TextSafetyScore"
        -> llm-gateway / "SpamClassification" [parallel with TextSafetyScore]
        -> [branch by score]
           approve path: product-service / "SaveReview"
           flag path:    notification-service / "AlertModerationQueue"
           block path:   [return 422 immediately]
  -> analytics-service / TrackEvent moderation.decision
```
Shape: Linear with parallel LLM calls + 3-way terminal branch

**Span count estimate:** 12-16

**Key attributes:**
```
content-moderation-service / "ModerateContent"
  moderation.content_type = "review_text"
  moderation.text_length = 50-2000
  moderation.decision = "approve" | "flag" | "block"
  moderation.policy_version = "v2.3"

llm-gateway / "TextSafetyScore"
  gen_ai.request.model = "gpt-4o-mini"
  gen_ai.usage.input_tokens = 100-600
  moderation.categories_checked = ["hate", "violence", "spam", "pii"]
  moderation.highest_score = 0.0-1.0
  moderation.threshold = 0.7
```

**Error modes:** LLM moderation API timeout -> fail open (approve with flag), PII detected in review text (trigger redaction span event), moderation score exactly at threshold boundary (A/B test of threshold value)

---

#### AI-11: Multi-Step Agent Orchestration (Research and Buy)

**Description:** A complex agentic flow where the user asks "Research the best running shoes under $150 and add the top pick to my cart." The agent runs multiple planning and execution cycles: web research simulation, product search, comparison, decision, and cart update.

**Services involved:** web-frontend, api-gateway, auth-service, ai-agent-service, llm-gateway, search-service, vector-db-service, embedding-service, cart-service, cache-service

**Trace topology:**
```
web-frontend (complex agent request)
  -> api-gateway
     -> auth-service
     -> ai-agent-service / "ExecuteAgentTask"
        LOOP (3-5 iterations):
          -> llm-gateway / "AgentPlanStep N"
             [outputs: tool to call, reasoning]
          -> [tool execution — varies per step]:
             Step 1: search-service / "ProductSearch"
             Step 2: embedding-service + vector-db-service / "SemanticFilter"
             Step 3: llm-gateway / "CompareOptions"
             Step 4: cart-service / "AddItem"
          -> llm-gateway / "AgentReflect" [check if goal complete]
        -> cache-service / SET agent result context
```
Shape: Iterative loop (plan->act->reflect) — creates self-similar repeating subtrace blocks

**Span count estimate:** 28-40 (loop iterations vary)

**Key attributes:**
```
ai-agent-service / "ExecuteAgentTask"
  agent.task = "research_and_buy"
  agent.max_iterations = 5
  agent.iterations_used = 3-5
  agent.goal_achieved = true | false
  agent.total_tokens_input = 2000-8000
  agent.total_tokens_output = 500-2000

llm-gateway / "AgentPlanStep N"
  gen_ai.request.model = "gpt-4o"
  agent.step_number = 1-5
  agent.reasoning = "need to search for products first"
  agent.tool_selected = "product_search"
  gen_ai.usage.input_tokens = 400-1200
  gen_ai.usage.output_tokens = 50-200
```

**Error modes:** Agent loop limit reached without goal completion -> `agent.goal_achieved = false`; tool call returned empty results -> agent replans; LLM produces circular plan (calls same tool twice with same params) -> loop detection guard triggers

**What makes it interesting for observability:** The iterative loop creates the most complex trace topology in the entire generator. Each iteration is a mini-trace. Token accumulation across iterations shows context window consumption. The `agent.iterations_used` attribute lets you correlate complexity with latency. This is the scenario that most clearly differentiates AI observability from traditional distributed tracing.

---

#### AI-12: Conversational Commerce (Multi-Turn Cart Manipulation)

**Description:** A multi-turn chat session where the user builds their cart through conversation. Each message is a separate trace that shares a session context via cache. Demonstrates stateful AI over stateless HTTP.

**Services involved:** web-frontend, api-gateway, auth-service, ai-agent-service, llm-gateway, cart-service, product-service, cache-service

**Trace topology (per message):**
```
web-frontend (chat message N)
  -> api-gateway
     -> auth-service
     -> cache-service / "GET conversation_context:session_id" (prior turns)
     -> ai-agent-service / "ProcessTurn"
        -> llm-gateway / "ChatCompletion" (full history in context)
        -> [conditional tool calls]:
           -> cart-service / "AddItem" | "RemoveItem" | "UpdateQuantity"
           -> product-service / "GetProduct" (for price confirmation)
     -> cache-service / "SET conversation_context" (append new turn)
```
Shape: Short linear per turn, but cross-trace session linkage via `agent.session_id`

**Span count estimate:** 10-14 per turn

**Key attributes:**
```
ai-agent-service / "ProcessTurn"
  agent.session_id = "sess_b9f2a..."
  agent.turn_number = 1-10
  agent.context_tokens = 200-3800 (grows each turn)
  agent.cart_action = "add" | "remove" | "clear" | "none"

llm-gateway / "ChatCompletion"
  gen_ai.request.model = "gpt-4o-mini"
  gen_ai.usage.input_tokens = 200-3800 (growing)
  gen_ai.usage.output_tokens = 50-300
  gen_ai.request.max_tokens = 512
```

**Error modes:** Context window full (approaching 128k token limit) -> context summarization sub-call; cart manipulation conflicts with concurrent session

---

### Part 3: Exception Library Additions

Each AI scenario requires new exception types. Add these to the exception variable block:

```go
var llmExceptions = []exceptionInfo{
    {"openai.APIError", "rate_limit_exceeded: Rate limit reached for gpt-4o in organization org-... on tokens per min. Limit: 90000, Used: 89742, Requested: 1200.", "..."},
    {"openai.APIError", "context_length_exceeded: This model's maximum context length is 128000 tokens. However, your messages resulted in 129341 tokens.", "..."},
    {"openai.APITimeoutError", "Request timed out after 30000ms waiting for first token from gpt-4o", "..."},
    {"openai.APIConnectionError", "Connection error: Failed to establish connection to api.openai.com", "..."},
}

var vectorDBExceptions = []exceptionInfo{
    {"qdrant.QdrantException", "Collection 'product-embeddings' not found", "..."},
    {"qdrant.QdrantException", "Timeout waiting for search results after 5000ms", "..."},
    {"qdrant.QdrantException", "Dimension mismatch: expected 1536, got 768", "..."},
}

var agentExceptions = []exceptionInfo{
    {"agent.MaxIterationsExceeded", "Agent reached maximum iteration limit (5) without completing goal", "..."},
    {"agent.HallucinatedTool", "LLM requested unknown tool 'get_competitor_price' not in tool registry", "..."},
    {"agent.TokenBudgetExceeded", "Agent token budget of 10000 exceeded after 3 iterations (used: 10247)", "..."},
}
```

---

### Part 4: Architecture Recommendations

#### File Splitting Decision

**Evidence:**
- Current file: 2,347 lines
- Current scenario lines (15 scenarios): 1,935
- Average lines per scenario: 129
- Average for AI scenarios (heavier): estimated 180-220
- Projected total at 40 scenarios: ~6,000-6,500 lines

**Finding:** At 30+ scenarios the file exceeds 4,500 lines and at 40+ scenarios exceeds 6,000 lines. This is not a correctness problem — Go has no file-size limit — but it creates a navigation and maintainability problem. The Go toolchain (gopls, go build) handles single-file packages fine. The issue is human: finding a specific scenario in a 6,000-line file requires grep.

**Recommendation: Split by category into packages within the same module.**

Proposed structure:

```
cmd/tracegen/
  main.go                     # bootstrap only: flags, providers, ticker, scenario dispatch
  scenarios/
    traditional/
      order.go                # createOrderFlow, fullCheckoutFlow, addToCartFlow
      auth.go                 # userLoginFlow, userRegistrationFlow
      payment.go              # failedPaymentFlow, sagaCompensationFlow, returnRefundFlow
      search.go               # searchAndBrowseFlow, timeoutCascadeFlow, multiCurrencyFlow
      notifications.go        # bulkNotificationFlow, shippingUpdateFlow, subscriptionFlow
      admin.go                # inventorySyncFlow, scheduledReportFlow, adminProductCRUDFlow
      catalog.go              # recommendationFlow, productReviewFlow, wishlistFlow
      commerce.go             # couponFlow, giftCardFlow, orderHistoryFlow, supportTicketFlow
      experimental.go         # healthCheckFlow, rateLimitFlow, abTestFlow, stripeWebhookFlow
    ai/
      rag.go                  # aiSemanticSearchFlow (AI-01)
      chatbot.go              # aiChatbotToolUseFlow (AI-02), conversationalCommerceFlow (AI-12)
      content.go              # aiContentGenerationFlow (AI-03), aiContentModerationFlow (AI-10)
      agents.go               # aiPricingAgentFlow (AI-04), aiInventoryAgentFlow (AI-05),
                              # aiMultiStepAgentFlow (AI-11)
      support.go              # aiCustomerSupportFlow (AI-06)
      ml.go                   # aiEmbeddingPipelineFlow (AI-07), aiModelRetrainingFlow (AI-09)
      fraud.go                # aiEnhancedFraudFlow (AI-08)
    common/
      helpers.go              # sleep, randomIP, randomUserID, randomOrderID, randomSearchTerm
      exceptions.go           # all exception variable definitions
      providers.go            # newProvider, tracerPool, pods definition
```

**Trade-off analysis for file split:**

| Criterion (Weight) | Single File | Multi-File Package Split |
|--------------------|-------------|--------------------------|
| Build simplicity (20%) | 9 — zero change | 8 — minor refactor of var globals |
| Navigability at 40+ scenarios (25%) | 3 — grep-only | 9 — category-clear |
| Contributor onboarding (20%) | 4 — overwhelming | 9 — each file is self-contained |
| Cross-scenario code sharing (15%) | 8 — all vars visible | 7 — need exported helpers |
| Git blame / PR isolation (20%) | 3 — every AI PR touches main file | 9 — changes in their own file |
| **Weighted Total** | **5.55** | **8.45** |

**Decision:** The multi-file package split wins decisively. The refactor is low-risk: it is purely mechanical (move functions + export helpers). No behavior changes.

**When to split:** Before adding more than 5 new scenarios. Splitting at 20 scenarios is significantly easier than splitting at 35. The recommended threshold is: split before the file exceeds 3,000 lines.

#### Infrastructure Patterns for AI Scenarios

**New infrastructure spans needed:**

1. **Streaming simulation:** LLM streaming responses (SSE) cannot be modeled as a single HTTP span without distorting latency semantics. Recommended approach: use `SpanKindServer` for the LLM gateway span with a `llm.streaming = true` attribute and model the time-to-first-token vs. total-time as two separate timing attributes. Do not try to emit a span per token.

2. **Agent loop spans:** Each agent iteration should be a child span of the parent `ExecuteAgentTask` span. Use `SpanKindInternal` for planning/reflection steps. This creates the self-similar nested topology that is most interesting in 3D visualization.

3. **Context propagation across agent turns:** For multi-turn chatbot scenarios (AI-12), the `agent.session_id` attribute links traces across HTTP requests. Some backends support session-level trace grouping. Emit a dedicated `agent.session_id` attribute on every turn's root span.

4. **Token budget as a resource attribute:** For AI observability, `gen_ai.usage.input_tokens` and `gen_ai.usage.output_tokens` are the cost signal. Ensure every LLM call emits these, even in the fast non-streaming path.

5. **No new gRPC streaming or WebSocket spans are required** for the proposed scenarios. All AI scenarios use HTTP/gRPC request-response semantics from the tracer's perspective. The "streaming" is internal to the LLM gateway service and does not need to be surfaced as a separate span kind in this generator.

#### New Service Pod Assignments (Proposed)

```go
// llm-gateway (3 replicas — high traffic, all paths through it)
{"llm-gateway", "llm-gw-a23b4c-k7mn2", "aks-userpool2-52891647-vmss000000"},
{"llm-gateway", "llm-gw-a23b4c-p9qr5", "aks-userpool1-38437823-vmss000001"},
{"llm-gateway", "llm-gw-a23b4c-h4st8", "aks-userpool1-38437823-vmss000002"},
// embedding-service (2 replicas)
{"embedding-service", "embed-svc-b34c5d-j2vw6", "aks-userpool1-38437823-vmss000000"},
{"embedding-service", "embed-svc-b34c5d-m8xy3", "aks-userpool2-52891647-vmss000001"},
// vector-db-service (2 replicas)
{"vector-db-service", "vecdb-svc-c45d6e-n5ab9", "aks-userpool2-52891647-vmss000001"},
{"vector-db-service", "vecdb-svc-c45d6e-q7cd4", "aks-userpool1-38437823-vmss000002"},
// ai-agent-service (2 replicas)
{"ai-agent-service", "agent-svc-d56e7f-r3ef1", "aks-userpool1-38437823-vmss000000"},
{"ai-agent-service", "agent-svc-d56e7f-t6gh7", "aks-userpool2-52891647-vmss000002"},
// content-moderation-service (2 replicas)
{"content-moderation-service", "modsvc-e67f8a-u9ij5", "aks-userpool1-38437823-vmss000001"},
{"content-moderation-service", "modsvc-e67f8a-v2kl8", "aks-userpool2-52891647-vmss000000"},
// model-registry-service (1 replica — singleton)
{"model-registry-service", "modelreg-f78a9b-w5mn3", "aks-userpool1-38437823-vmss000002"},
// feature-store-service (2 replicas)
{"feature-store-service", "featstore-a89b1c-x8op6", "aks-userpool1-38437823-vmss000001"},
{"feature-store-service", "featstore-a89b1c-y1qr9", "aks-userpool2-52891647-vmss000000"},
// data-pipeline-service (2 replicas)
{"data-pipeline-service", "pipeline-b91c2d-z4st2", "aks-userpool1-38437823-vmss000002"},
{"data-pipeline-service", "pipeline-b91c2d-a7uv5", "aks-userpool2-52891647-vmss000001"},
```

---

## L2: Architectural Implications (Principal Architect)

### Systemic Patterns Identified

**Pattern 1: The file is a monolith by accumulation, not by design.**

The current single-file architecture is correct for 15 scenarios at 2,347 lines. It becomes a liability above 35 scenarios at ~5,500 lines. This is a well-understood software entropy pattern: a file that starts as a convenient single unit accumulates content until navigation cost exceeds organizational benefit. The inflection point for this codebase, given average scenario complexity, is approximately 30 scenarios (3,800-4,000 lines). Splitting before that point costs ~2 hours of refactoring. Splitting after costs significantly more due to merge conflicts and cognitive load.

**Pattern 2: AI scenarios create a qualitatively different observability substrate.**

Traditional scenarios produce trees of known depth and width. AI scenarios produce trees of variable and sometimes unbounded depth (agent loops), trees with token-count attributes that carry economic meaning (cost per trace), and traces that are linked across HTTP request boundaries by session ID rather than trace context propagation. These are not just "more spans" — they require observability backends to support new query patterns: cost attribution by model, session-level trace grouping, iteration-count distribution, and safety filter event analysis. The generator should emit attributes that enable these queries even if not all backends support them today.

**Pattern 3: The llm-gateway is the correct centralization point for AI telemetry.**

All AI scenarios should route through `llm-gateway` rather than having individual services call OpenAI directly. This mirrors how production AI systems are actually built (for rate limiting, key management, audit) and concentrates the `gen_ai.*` attribute emission in one service. For the trace generator specifically, this means AI observability attributes are consistently present and comparable across all AI scenarios. It also creates a natural chokepoint for error injection: a single `llm-gateway` error rate knob can affect all AI scenarios simultaneously.

**Pattern 4: Scenario categorization should drive both file structure and `isError` classification.**

The current `isError` boolean (which excludes a scenario when `-errors=0`) is applied only to three scenarios. As scenarios grow to 40+, a richer classification is needed. Proposed extension: add an `aiScenario bool` flag and an optional `-ai` flag to enable/disable the AI scenario category. This allows users who just want traditional e-commerce traces (for simple demos) to exclude the AI scenarios without modifying code.

### Long-Term Architectural Considerations

**Consideration 1: Scenario registry as data, not code.**

At 40+ scenarios, the `allScenarios` slice in `main()` becomes a long list. Consider moving scenario metadata (name, function reference, isError, category) to a struct with a `Register()` pattern where each scenario file registers itself via `init()`. This avoids a centralized list that every new scenario must edit.

**Consideration 2: OTel Semantic Conventions for Generative AI (semconv experimental).**

The `gen_ai.*` namespace is published as experimental in OTel semconv v1.27+. The current `go.mod` references semconv v1.26.0 (via the import path `semconv "go.opentelemetry.io/otel/semconv/v1.26.0"`). Before implementing AI scenarios, update to v1.27+ to use the published `gen_ai` attribute constants rather than raw strings. This makes the AI spans forward-compatible with backends that auto-parse `gen_ai.*` attributes.

**Consideration 3: The `consumersEnabled` flag pattern should extend to AI scenarios.**

The existing `-no-consumers` flag disables all async message consumers, creating a "messages pile up" failure mode. A parallel `-no-ai-backends` flag could simulate "LLM API unavailable" — all AI spans would emit with error status and `errorChance` would always fire for AI-specific calls. This doubles the value of the AI scenarios for chaos testing.

**Consideration 4: Binary size and build time.**

Adding 25 new scenarios and 8 new services will increase the binary by less than 5% (Go spans are pure data structures, no embedded assets). Build time will increase proportionally to added lines but remain well under 10 seconds for `go build`. The goreleaser pipeline requires no changes.

**Consideration 5: Prevention strategy for future bloat.**

Establish a scenario complexity budget:
- Simple scenarios (health check, rate limit pattern): max 80 lines
- Standard scenarios (search, login, review): max 150 lines
- Complex scenarios (checkout, saga): max 300 lines
- AI agent scenarios: max 350 lines

Review during PR: if a new scenario exceeds its budget, decompose it into two scenarios.

### Implementation Priority Order

Based on observability value, novelty of trace topology, and AI pattern coverage:

| Priority | Scenario | Justification |
|----------|----------|---------------|
| 1 | AI-01: RAG Search | Introduces embedding + vector DB spans; foundational AI pattern |
| 2 | AI-02: Chatbot Tool Use | Introduces agent planning spans; most realistic AI use case |
| 3 | AI-10: Content Moderation | Introduces safety filter branch; high demand for this pattern |
| 4 | T-03: Return/Refund | Fills obvious gap; reverse money flow trace is frequently requested |
| 5 | AI-11: Multi-Step Agent | Most complex topology; differentiates the generator from all alternatives |
| 6 | T-01: User Registration | Completes user lifecycle (login exists, registration missing) |
| 7 | AI-06: AI Support Agent | Combines classification + escalation; realistic enterprise AI |
| 8 | T-05: Coupon Application | Common e-commerce flow; validates cart recalculation chain |
| 9 | AI-07: Embedding Pipeline | High fan-out; stress tests trace ingestion at volume |
| 10 | T-08: A/B Test Exposure | Legitimizes config-service as experiment infrastructure |
| **File split** | **Refactor** | **Do before adding scenarios 11-25** |

---

## Conclusions

1. **The code quality is high and the architecture is sound for its current scope.** The patterns used (errorChance, consumersEnabled, goroutine fan-out with channels, headless entry points) are all reusable as-is for new scenarios. No refactoring of existing code is required before adding new scenarios.

2. **The file must be split before reaching 30 scenarios.** Evidence: at the current average of 129 lines/scenario (conservative for AI scenarios at ~200 lines each), 30 scenarios total will produce 4,300+ lines; 40 scenarios will produce 6,000+ lines. The multi-file package split has a weighted score of 8.45 vs. 5.55 for the single-file approach.

3. **AI scenarios require 8 new services (16 new pods) and 3 new exception type arrays.** The `gen_ai.*` semantic convention namespace should be adopted from semconv v1.27+. All new AI service pods fit within the existing 5-node AKS topology.

4. **The llm-gateway is the correct architectural centralization point** for all AI observability. Every AI scenario should route LLM calls through it.

5. **Token counting is the new spans-per-trace for AI.** Every LLM span must emit `gen_ai.usage.input_tokens` and `gen_ai.usage.output_tokens` to enable cost attribution analysis in downstream backends.

6. **The multi-step agent scenario (AI-11) is the highest-value single addition** for demonstrating AI observability differentiation. It creates a topological pattern (iterative plan->act->reflect loop) that does not exist in any current trace generator.

---

## Recommendations

**Immediate (before any new scenarios):**
- R-1: Update `go.mod` semconv import to v1.27+ for `gen_ai.*` attribute constants
- R-2: Plan the file split into `scenarios/traditional/` and `scenarios/ai/` subdirectories — execute before adding more than 5 new scenarios

**Short term (first 10 new scenarios):**
- R-3: Implement AI-01 (RAG Search) first to establish the embedding + vector DB span pattern
- R-4: Implement AI-02 (Chatbot Tool Use) second to establish the agent planning loop pattern
- R-5: Add the 8 new service pod definitions with realistic instance IDs and node assignments
- R-6: Add `llmExceptions`, `vectorDBExceptions`, and `agentExceptions` arrays

**Medium term (scenarios 11-25):**
- R-7: Add `-no-ai-backends` flag to complement `-no-consumers`
- R-8: Add `category` field to `namedScenario` struct and a `-category` filter flag
- R-9: Add scenario complexity budget enforcement to PR review checklist

**Long term (scenarios 26-40):**
- R-10: Migrate scenario registration to `init()`-based auto-registration to avoid centralized list maintenance

---

## Evidence Summary

| Evidence ID | Type | Source | Relevance |
|-------------|------|--------|-----------|
| E-001 | Code measurement | `cmd/tracegen/main.go`, `wc -l` | 2,347 lines current |
| E-002 | Code analysis | `grep "^func"` | 15 scenario functions, 12 helper/infra functions |
| E-003 | Code analysis | Function boundary line numbers | Average 129 lines/scenario |
| E-004 | Code analysis | `grep "tracer("` | 138 span creation calls across 15 scenarios |
| E-005 | Code analysis | Pod list, lines 132-196 | 20 services, 43 pods, 5 AKS nodes |
| E-006 | Code analysis | `fullCheckoutFlow`, lines 1423-1729 | Monster chain pattern: 310 lines, 16 services |
| E-007 | Code analysis | `sagaCompensationFlow`, lines 1850-2089 | 4-way parallel compensation fan-out |
| E-008 | Code analysis | `recommendationFlow`, lines 1174-1295 | Scatter-gather pattern with goroutines |
| E-009 | Code analysis | `timeoutCascadeFlow`, lines 2094-2194 | Circuit breaker + stale cache fallback |
| E-010 | Code analysis | `healthCheckFlow`, lines 800-858 | Concurrent fan-out with channel sync |
| E-011 | Code analysis | ML span in `fullCheckoutFlow`, lines 1578-1587 | Existing `ml.model`, `ml.features` attributes |
| E-012 | Code analysis | `errorChance()`, line 86-88 | Reusable error injection mechanism |
| E-013 | Code analysis | `consumersEnabled`, line 82 | Reusable for `-no-ai-backends` flag pattern |
| E-014 | Projection | Node.js calculation | 30 scenarios: ~4,283 lines; 40 scenarios: ~5,800-6,500 lines |
| E-015 | go.mod | `go.mod`, line 19 | semconv v1.26.0 — upgrade needed for gen_ai.* constants |
| E-016 | README.md | Feature comparison table | Single-binary constraint must be maintained |
| E-017 | goreleaser.yml | Build matrix | Cross-platform binary; no new dependencies allowed unless CGO_ENABLED=0 compatible |

---

## PS Integration

```yaml
analyst_output:
  ps_id: "ps-tracegen-001"
  entry_id: "e-001"
  analysis_type: "gap+trade-off+impact"
  artifact_path: "docs/analysis/ps-tracegen-001-e-001-scenario-expansion.md"
  root_cause: "N/A — gap analysis"
  key_findings:
    - "File split required before 30 scenarios (projected 4,300+ lines)"
    - "12 AI scenarios proposed, 8 new services needed (16 pods)"
    - "gen_ai.* semconv namespace adoption required (semconv v1.27+)"
    - "llm-gateway is correct centralization point for all AI telemetry"
    - "Multi-step agent (AI-11) is highest-value addition for AI observability differentiation"
  recommendation: "Split file first, then implement in priority order: AI-01 RAG, AI-02 Chatbot, AI-10 Moderation"
  confidence: "high"
  next_agent_hint: "ps-architect for Go package structure design; implementation"
```
