# Implementation Spec: AI Agentic & Traditional Scenario Expansion

**PS ID:** ps-tracegen-001
**Entry ID:** e-002
**Type:** Implementation specification
**Date:** 2026-03-07
**Input:** e-001 (scenario expansion analysis), LLM observability market research (SP-023), Microsoft Semantic Kernel / Agent Framework OTel instrumentation, OTel GenAI Semantic Conventions registry

---

## L0: Executive Summary

This spec tells a developer exactly what to build, in what order, and with what attributes. The goal is to grow from 15 to 40 scenario flows by adding 13 traditional e-commerce and 12 AI agentic scenarios, plus 8 new services (59 total pods).

The AI scenarios are designed to generate the exact same telemetry signals that every LLM observability tool on the market tracks (Langfuse, LangSmith, Helicone, Arize, Traceloop, Portkey, Galileo) AND match the exact span/attribute shapes emitted by Microsoft Semantic Kernel and Microsoft Agent Framework -- the two most widely adopted .NET AI frameworks. This means IAPM can demonstrate it visualizes LLM traces just as well as dedicated LLM APM tools -- but unified with traditional distributed traces in 3D.

**Key insight from market research:** Every LLM APM tool tracks token usage, cost attribution, model names, and evaluations -- but NONE of them provide traditional APM. IAPM bridges this gap. The trace generator must produce spans that prove this.

**Key insight from Microsoft Agent Framework:** Their OTel instrumentation emits exactly three span types: `invoke_agent {name}`, `chat {model}`, and `execute_tool {function}`. Our AI scenarios should produce traces that look structurally identical to what a real Semantic Kernel / Agent Framework app would emit.

---

## L1: OTel GenAI Attribute Contract

### What LLM APM Tools Track (Market Evidence)

Source: SP-023 tools-analysis.md -- analysis of 8 LLM observability platforms.

Every tool in the market tracks these signals. Our AI scenarios MUST emit all of them:

| Signal | OTel GenAI Attribute | Who Tracks It | Our Span Location |
|--------|---------------------|---------------|-------------------|
| **Token usage (input)** | `gen_ai.usage.input_tokens` | All 8 tools | Every `llm-gateway` span |
| **Token usage (output)** | `gen_ai.usage.output_tokens` | All 8 tools | Every `llm-gateway` span |
| **Model name** | `gen_ai.request.model` | All 8 tools | Every `llm-gateway` span |
| **LLM provider** | `gen_ai.system` | All 8 tools | Every `llm-gateway` span |
| **Finish reason** | `gen_ai.response.finish_reason` | Langfuse, LangSmith, Arize, Traceloop | Every `llm-gateway` completion span |
| **Temperature** | `gen_ai.request.temperature` | Langfuse, LangSmith, Helicone | Content generation spans |
| **Max tokens** | `gen_ai.request.max_tokens` | Langfuse, LangSmith, Helicone | Chat/completion spans |
| **Response ID** | `gen_ai.response.id` | Langfuse, LangSmith | Every `llm-gateway` span |
| **Cost** | Derived from tokens x model pricing | Langfuse, Helicone (300+ models), Portkey | Backend-computed from token counts |
| **Prompt management** | `gen_ai.prompt.id`, `gen_ai.prompt.version` | Langfuse, LangSmith, W&B | Agent service spans (optional) |
| **Evaluations/evals** | Custom attributes (`eval.score`, `eval.judge_model`) | Langfuse, LangSmith, Arize, Galileo | Moderation + fraud explanation spans |
| **Guardrails** | Custom attributes (`guardrail.triggered`, `guardrail.action`) | Portkey (50+), Galileo | Content moderation, token budget, agent loop limit |

### Reference: Microsoft Semantic Kernel / Agent Framework Span Shapes

Source: [Microsoft Agent Framework Observability](https://learn.microsoft.com/en-us/agent-framework/tutorials/agents/enable-observability), [Semantic Kernel Observability](https://learn.microsoft.com/en-us/semantic-kernel/concepts/enterprise-readiness/observability/)

Microsoft's production .NET AI frameworks emit exactly three span types:

| Span Name Pattern | SpanKind | Operation | Example |
|-------------------|----------|-----------|---------|
| `invoke_agent {agent_name}` | CLIENT | Agent invocation | `invoke_agent WeatherAgent` |
| `chat {model_name}` | CLIENT | LLM call | `chat gpt-4o` |
| `execute_tool {function_name}` | INTERNAL | Tool execution | `execute_tool get_weather` |

Their actual trace output (from Agent Framework docs):
```json
{
    "name": "invoke_agent Joker",
    "kind": "SpanKind.CLIENT",
    "attributes": {
        "gen_ai.operation.name": "invoke_agent",
        "gen_ai.system": "openai",
        "gen_ai.agent.id": "Joker",
        "gen_ai.agent.name": "Joker",
        "gen_ai.request.instructions": "You are good at telling jokes.",
        "gen_ai.response.id": "chatcmpl-CH6fgKwMRGDtGNO3H88gA3AG2o7c5",
        "gen_ai.usage.input_tokens": 26,
        "gen_ai.usage.output_tokens": 29
    }
}
```

Their metrics:
- `gen_ai.client.operation.duration` (histogram) - seconds
- `gen_ai.client.token.usage` (histogram) - token count
- `agent_framework.function.invocation.duration` (histogram) - tool execution time

Activity sources to listen for: `*Microsoft.Extensions.AI` (chat client), `*Microsoft.Extensions.Agents*` (agent)

**Our trace generator should produce spans that look structurally identical to this output.**

### Mandatory Attributes for Every LLM Gateway Span

Source: [OTel GenAI Attribute Registry](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/), [OTel GenAI Client Spans](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/)

**Span naming convention:** `{gen_ai.operation.name} {gen_ai.request.model}` (e.g., `chat gpt-4o`, `embedding text-embedding-3-small`)

Every span emitted by `llm-gateway` MUST include these attributes (non-negotiable for LLM APM parity):

```go
// REQUIRED on every llm-gateway span (per OTel spec + MS Agent Framework alignment)
attribute.String("gen_ai.operation.name", "chat"),                     // chat|embedding|create_agent|invoke_agent|execute_tool
attribute.String("gen_ai.system", "openai"),                           // backward compat (MS Agent Framework uses this)
attribute.String("gen_ai.provider.name", "openai"),                    // current OTel spec (gen_ai.system -> gen_ai.provider.name migration)
attribute.String("gen_ai.request.model", "gpt-4o"),                    // model requested
attribute.String("gen_ai.response.model", "gpt-4o-2024-08-06"),       // model that responded (may differ)
attribute.Int("gen_ai.usage.input_tokens", inputTokens),               // prompt tokens consumed
attribute.Int("gen_ai.usage.output_tokens", outputTokens),             // completion tokens produced
attribute.StringSlice("gen_ai.response.finish_reasons", []string{"stop"}), // NOTE: array, not singular! stop|length|tool_calls|content_filter
attribute.String("gen_ai.response.id", "chatcmpl-"+randomHex(24)),    // response ID
```

### Additional Attributes by Operation Type

**Chat completions** (`gen_ai.operation.name = "chat"`):

Span name: `chat gpt-4o`

```go
attribute.Float64("gen_ai.request.temperature", 0.7),
attribute.Int("gen_ai.request.max_tokens", 512),
attribute.Float64("gen_ai.request.top_p", 1.0),
attribute.Float64("gen_ai.request.frequency_penalty", 0.0),         // recommended
attribute.Float64("gen_ai.request.presence_penalty", 0.0),          // recommended
```

**Embeddings** (`gen_ai.operation.name = "embedding"`):

Span name: `embedding text-embedding-3-small`

```go
attribute.String("gen_ai.operation.name", "embedding"),
attribute.String("gen_ai.request.model", "text-embedding-3-small"),
attribute.Int("gen_ai.usage.input_tokens", batchTokens),
// Note: embedding operations have no output_tokens
attribute.Int("gen_ai.embeddings.dimension.count", 1536),            // correct attribute name per registry
attribute.StringSlice("gen_ai.request.encoding_formats", []string{"float"}),
```

**Tool calls / function calling** (finish_reason = "tool_calls"):
```go
attribute.StringSlice("gen_ai.response.finish_reasons", []string{"tool_calls"}),
```

**Execute tool spans** (`gen_ai.operation.name = "execute_tool"`):

Span name: `execute_tool get_order_status`
SpanKind: INTERNAL

```go
attribute.String("gen_ai.operation.name", "execute_tool"),
attribute.String("gen_ai.tool.name", "get_order_status"),
attribute.String("gen_ai.tool.type", "function"),                    // function|extension|datastore
attribute.String("gen_ai.tool.call.id", "call_"+randomHex(24)),
attribute.String("gen_ai.tool.description", "Get the status of a customer order"),
```

**Agent invocation spans** (`gen_ai.operation.name = "invoke_agent"`):

Span name: `invoke_agent CustomerSupportAgent`
SpanKind: CLIENT (remote) or INTERNAL (in-process)

```go
attribute.String("gen_ai.operation.name", "invoke_agent"),
attribute.String("gen_ai.agent.id", agentID),
attribute.String("gen_ai.agent.name", "CustomerSupportAgent"),
attribute.String("gen_ai.agent.description", "Handles customer inquiries about orders and products"),
attribute.String("gen_ai.agent.version", "1.0.0"),
attribute.String("gen_ai.conversation.id", sessionID),               // links multi-turn interactions
attribute.Int("gen_ai.usage.input_tokens", totalInputTokens),
attribute.Int("gen_ai.usage.output_tokens", totalOutputTokens),
```

**Retrieval / RAG spans** (`gen_ai.operation.name = "retrieve"`):

Span name: `retrieve product-embeddings`
SpanKind: CLIENT

```go
attribute.String("gen_ai.operation.name", "retrieve"),
attribute.String("gen_ai.data_source.id", "product-embeddings"),     // per OTel agent span spec
attribute.Float64("gen_ai.request.top_k", 20),
```

### Evaluation and Guardrail Attributes

Source: [OTel GenAI Registry](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/)

```go
// Evaluation attributes (for moderation scoring, fraud explanation)
attribute.String("gen_ai.evaluation.name", "content_safety"),
attribute.Float64("gen_ai.evaluation.score.value", 0.85),
attribute.String("gen_ai.evaluation.score.label", "flagged"),
attribute.String("gen_ai.evaluation.explanation", "Content flagged for potential spam"),
```

---

## L1: Helper Functions for GenAI Spans

To ensure consistency across all AI scenarios AND match Microsoft Semantic Kernel / Agent Framework output, create these helpers:

### Chat Completion Helper

Span name follows OTel convention: `chat {model}` (matches MS Agent Framework)

```go
// chatSpan creates a "chat {model}" span on llm-gateway with standard gen_ai attributes.
// Matches the exact span shape emitted by Microsoft Semantic Kernel and Agent Framework.
func chatSpan(ctx context.Context, model string, inputTokens, outputTokens int, finishReasons ...string) (context.Context, trace.Span) {
    if len(finishReasons) == 0 {
        finishReasons = []string{"stop"}
    }
    ctx, span := tracer("llm-gateway").Start(ctx, "chat "+model,
        trace.WithSpanKind(trace.SpanKindClient),
        trace.WithAttributes(
            // Core gen_ai attributes (required per OTel spec + MS Agent Framework)
            attribute.String("gen_ai.operation.name", "chat"),
            attribute.String("gen_ai.system", "openai"),             // MS Agent Framework uses this
            attribute.String("gen_ai.provider.name", "openai"),      // current OTel spec
            attribute.String("gen_ai.request.model", model),
            attribute.String("gen_ai.response.model", model),
            attribute.Int("gen_ai.usage.input_tokens", inputTokens),
            attribute.Int("gen_ai.usage.output_tokens", outputTokens),
            attribute.StringSlice("gen_ai.response.finish_reasons", finishReasons),
            attribute.String("gen_ai.response.id", "chatcmpl-"+randomHex(24)),
            // HTTP context for the upstream API call
            attribute.String("http.method", "POST"),
            attribute.String("http.url", "https://api.openai.com/v1/chat/completions"),
            attribute.String("peer.service", "openai-api"),
            attribute.Int("http.status_code", 200),
        ),
    )
    return ctx, span
}
```

### Embedding Helper

Span name: `embedding {model}` (per OTel convention)

```go
// embeddingSpan creates an "embedding {model}" span for text-to-vector operations.
func embeddingSpan(ctx context.Context, model string, inputTokens, dimensions int) (context.Context, trace.Span) {
    ctx, span := tracer("llm-gateway").Start(ctx, "embedding "+model,
        trace.WithSpanKind(trace.SpanKindClient),
        trace.WithAttributes(
            attribute.String("gen_ai.operation.name", "embedding"),
            attribute.String("gen_ai.system", "openai"),
            attribute.String("gen_ai.provider.name", "openai"),
            attribute.String("gen_ai.request.model", model),
            attribute.String("gen_ai.response.model", model),
            attribute.Int("gen_ai.usage.input_tokens", inputTokens),
            attribute.Int("gen_ai.embeddings.dimension.count", dimensions),
            attribute.StringSlice("gen_ai.request.encoding_formats", []string{"float"}),
            attribute.String("gen_ai.response.id", "embd-"+randomHex(24)),
            attribute.String("http.method", "POST"),
            attribute.String("http.url", "https://api.openai.com/v1/embeddings"),
            attribute.String("peer.service", "openai-api"),
            attribute.Int("http.status_code", 200),
        ),
    )
    return ctx, span
}
```

### Agent Invocation Helper

Span name: `invoke_agent {name}` (matches MS Agent Framework exactly)

```go
// agentSpan creates an "invoke_agent {name}" span matching MS Agent Framework output.
func agentSpan(ctx context.Context, agentName, agentID, description, sessionID string) (context.Context, trace.Span) {
    ctx, span := tracer("ai-agent-service").Start(ctx, "invoke_agent "+agentName,
        trace.WithSpanKind(trace.SpanKindClient),
        trace.WithAttributes(
            attribute.String("gen_ai.operation.name", "invoke_agent"),
            attribute.String("gen_ai.system", "openai"),
            attribute.String("gen_ai.provider.name", "openai"),
            attribute.String("gen_ai.agent.id", agentID),
            attribute.String("gen_ai.agent.name", agentName),
            attribute.String("gen_ai.agent.description", description),
            attribute.String("gen_ai.agent.version", "1.0.0"),
            attribute.String("gen_ai.conversation.id", sessionID),
        ),
    )
    return ctx, span
}
```

### Tool Execution Helper

Span name: `execute_tool {name}` (matches MS Agent Framework exactly)

```go
// toolSpan creates an "execute_tool {name}" span for agent tool calls.
func toolSpan(ctx context.Context, toolName, toolDescription string) (context.Context, trace.Span) {
    ctx, span := tracer("ai-agent-service").Start(ctx, "execute_tool "+toolName,
        trace.WithSpanKind(trace.SpanKindInternal),
        trace.WithAttributes(
            attribute.String("gen_ai.operation.name", "execute_tool"),
            attribute.String("gen_ai.tool.name", toolName),
            attribute.String("gen_ai.tool.type", "function"),
            attribute.String("gen_ai.tool.call.id", "call_"+randomHex(24)),
            attribute.String("gen_ai.tool.description", toolDescription),
        ),
    )
    return ctx, span
}
```

### Utility

```go
func randomHex(n int) string {
    b := make([]byte, n/2)
    rand.Read(b)
    return fmt.Sprintf("%x", b)
}
```

---

## L1: New Services Pod Definitions

Add to the pod list (8 new services, 16 new pods, total 59):

```go
// llm-gateway (3 replicas - high traffic, all AI paths through it)
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
// model-registry-service (1 replica - singleton)
{"model-registry-service", "modelreg-f78a9b-w5mn3", "aks-userpool1-38437823-vmss000002"},
// feature-store-service (2 replicas)
{"feature-store-service", "featstore-a89b1c-x8op6", "aks-userpool1-38437823-vmss000001"},
{"feature-store-service", "featstore-a89b1c-y1qr9", "aks-userpool2-52891647-vmss000000"},
// data-pipeline-service (2 replicas)
{"data-pipeline-service", "pipeline-b91c2d-z4st2", "aks-userpool1-38437823-vmss000002"},
{"data-pipeline-service", "pipeline-b91c2d-a7uv5", "aks-userpool2-52891647-vmss000001"},
```

---

## L1: New Exception Arrays

```go
var llmExceptions = []exceptionInfo{
    {
        "OpenAI.RateLimitError",
        "rate_limit_exceeded: Rate limit reached for gpt-4o in organization org-abc123 on tokens per min (TPM). Limit: 90000, Used: 89742, Requested: 1200.",
        `OpenAI.RateLimitError: rate_limit_exceeded: Rate limit reached for gpt-4o in organization org-abc123 on tokens per min (TPM). Limit: 90000, Used: 89742, Requested: 1200.
   at LlmGateway.Clients.OpenAIClient.ChatCompletionAsync(ChatRequest request) in /src/LlmGateway/Clients/OpenAIClient.cs:line 87
   at LlmGateway.Services.ModelRouter.RouteRequestAsync(ModelRequest req) in /src/LlmGateway/Services/ModelRouter.cs:line 134`,
    },
    {
        "OpenAI.ContextLengthExceeded",
        "context_length_exceeded: This model's maximum context length is 128000 tokens. However, your messages resulted in 129341 tokens.",
        `OpenAI.ContextLengthExceeded: context_length_exceeded: This model's maximum context length is 128000 tokens. However, your messages resulted in 129341 tokens.
   at LlmGateway.Clients.OpenAIClient.ChatCompletionAsync(ChatRequest request) in /src/LlmGateway/Clients/OpenAIClient.cs:line 92
   at LlmGateway.Middleware.TokenBudgetGuard.ValidateAsync(ChatRequest req) in /src/LlmGateway/Middleware/TokenBudgetGuard.cs:line 55`,
    },
    {
        "OpenAI.APITimeoutError",
        "Request timed out after 30000ms waiting for response from gpt-4o",
        `OpenAI.APITimeoutError: Request timed out after 30000ms waiting for response from gpt-4o
   at LlmGateway.Clients.OpenAIClient.ChatCompletionAsync(ChatRequest request) in /src/LlmGateway/Clients/OpenAIClient.cs:line 103
   at System.Net.Http.HttpClient.SendAsync(HttpRequestMessage request, CancellationToken ct) in /_/src/System.Net.Http/HttpClient.cs:line 582`,
    },
    {
        "OpenAI.ContentFilterError",
        "content_filter: The response was filtered due to the prompt triggering Azure OpenAI's content management policy",
        `OpenAI.ContentFilterError: content_filter: The response was filtered due to the prompt triggering Azure OpenAI's content management policy
   at LlmGateway.Clients.OpenAIClient.ChatCompletionAsync(ChatRequest request) in /src/LlmGateway/Clients/OpenAIClient.cs:line 96
   at LlmGateway.Services.SafetyFilter.CheckResponseAsync(ChatResponse resp) in /src/LlmGateway/Services/SafetyFilter.cs:line 71`,
    },
}

var vectorDBExceptions = []exceptionInfo{
    {
        "Qdrant.QdrantException",
        "Collection 'product-embeddings' not found",
        `Qdrant.QdrantException: Collection 'product-embeddings' not found
   at VectorDbService.Clients.QdrantClient.SearchAsync(SearchRequest req) in /src/VectorDbService/Clients/QdrantClient.cs:line 63
   at VectorDbService.Services.VectorSearchService.SimilaritySearch(String collection, float[] vector, Int32 topK) in /src/VectorDbService/Services/VectorSearchService.cs:line 41`,
    },
    {
        "Qdrant.TimeoutException",
        "Timeout waiting for search results after 5000ms on collection 'product-embeddings'",
        `Qdrant.TimeoutException: Timeout waiting for search results after 5000ms on collection 'product-embeddings'
   at Qdrant.Client.QdrantGrpcClient.SearchAsync(SearchPoints request, CancellationToken ct) in /src/VectorDbService/Clients/QdrantClient.cs:line 78
   at System.Threading.Tasks.Task.WaitAsync(TimeSpan timeout) in /_/src/System.Threading/Tasks/Task.cs:line 3847`,
    },
    {
        "Qdrant.DimensionMismatch",
        "Dimension mismatch: expected 1536, got 768 for collection 'product-embeddings'",
        `Qdrant.DimensionMismatch: Dimension mismatch: expected 1536, got 768 for collection 'product-embeddings'
   at VectorDbService.Clients.QdrantClient.UpsertAsync(UpsertRequest req) in /src/VectorDbService/Clients/QdrantClient.cs:line 95
   at VectorDbService.Services.VectorIndexService.UpsertBatch(String collection, List<VectorPoint> points) in /src/VectorDbService/Services/VectorIndexService.cs:line 57`,
    },
}

var agentExceptions = []exceptionInfo{
    {
        "Agent.MaxIterationsExceeded",
        "Agent reached maximum iteration limit (5) without completing goal",
        `Agent.MaxIterationsExceeded: Agent reached maximum iteration limit (5) without completing goal
   at AiAgentService.Orchestrator.AgentLoop.ExecuteAsync(AgentTask task, Int32 maxIterations) in /src/AiAgentService/Orchestrator/AgentLoop.cs:line 112
   at AiAgentService.Services.AgentService.ProcessTaskAsync(String taskId) in /src/AiAgentService/Services/AgentService.cs:line 67`,
    },
    {
        "Agent.HallucinatedToolCall",
        "LLM requested tool 'get_competitor_price' which is not in the tool registry [get_order, get_product, get_tracking, search_products, add_to_cart, get_cart, get_user, check_inventory]",
        `Agent.HallucinatedToolCall: LLM requested tool 'get_competitor_price' which is not in the tool registry
   at AiAgentService.Orchestrator.ToolDispatcher.DispatchAsync(ToolCall call) in /src/AiAgentService/Orchestrator/ToolDispatcher.cs:line 45
   at AiAgentService.Orchestrator.AgentLoop.ExecuteToolStep(ToolCall call) in /src/AiAgentService/Orchestrator/AgentLoop.cs:line 89`,
    },
    {
        "Agent.TokenBudgetExceeded",
        "Agent token budget of 10000 exceeded after 3 iterations (used: 10247 input + 1893 output = 12140 total)",
        `Agent.TokenBudgetExceeded: Agent token budget of 10000 exceeded after 3 iterations (used: 10247 input + 1893 output = 12140 total)
   at AiAgentService.Middleware.TokenBudgetGuard.CheckBudget(AgentContext ctx) in /src/AiAgentService/Middleware/TokenBudgetGuard.cs:line 38
   at AiAgentService.Orchestrator.AgentLoop.ExecuteAsync(AgentTask task, Int32 maxIterations) in /src/AiAgentService/Orchestrator/AgentLoop.cs:line 78`,
    },
    {
        "Agent.CircularPlanDetected",
        "Agent produced identical plan 'search_products(query=laptop)' in consecutive iterations 3 and 4 -- loop detected",
        `Agent.CircularPlanDetected: Agent produced identical plan in consecutive iterations 3 and 4 -- loop detected
   at AiAgentService.Orchestrator.LoopDetector.CheckForCycle(List<AgentStep> steps) in /src/AiAgentService/Orchestrator/LoopDetector.cs:line 29
   at AiAgentService.Orchestrator.AgentLoop.ExecuteAsync(AgentTask task, Int32 maxIterations) in /src/AiAgentService/Orchestrator/AgentLoop.cs:line 95`,
    },
}

var moderationExceptions = []exceptionInfo{
    {
        "Moderation.ContentBlocked",
        "Content blocked: safety score 0.92 exceeds threshold 0.70 in category 'hate_speech'",
        `Moderation.ContentBlocked: Content blocked: safety score 0.92 exceeds threshold 0.70 in category 'hate_speech'
   at ContentModerationService.Classifiers.SafetyClassifier.ScoreAsync(String content) in /src/ContentModerationService/Classifiers/SafetyClassifier.cs:line 52
   at ContentModerationService.Services.ModerationPipeline.ProcessAsync(ModerationRequest req) in /src/ContentModerationService/Services/ModerationPipeline.cs:line 34`,
    },
    {
        "Moderation.PIIDetected",
        "PII detected in user content: email address pattern found at position 142 -- content requires redaction before storage",
        `Moderation.PIIDetected: PII detected in user content: email address pattern found at position 142
   at ContentModerationService.Classifiers.PIIDetector.ScanAsync(String content) in /src/ContentModerationService/Classifiers/PIIDetector.cs:line 67
   at ContentModerationService.Services.ModerationPipeline.ProcessAsync(ModerationRequest req) in /src/ContentModerationService/Services/ModerationPipeline.cs:line 41`,
    },
}
```

---

## L1: AI Scenario Implementation Details

### AI-01: Semantic Search with RAG

**Priority: 1** -- Establishes embedding + vector DB + LLM reranking pattern.

**Why this matters for IAPM:** This single scenario produces spans that Langfuse, Arize, and Traceloop all visualize: embedding token cost, vector search latency, LLM reranking token cost. IAPM shows them in 3D within the context of the full HTTP request chain.

```
Services: web-frontend -> api-gateway -> auth-service -> embedding-service ->
          llm-gateway [embed] -> vector-db-service -> llm-gateway [rerank] ->
          cache-service -> analytics-service
Shape: Linear with two sequential LLM calls
Spans: 14-16
```

**Key spans with full attribute contract:**

| Span | Service | Key Attributes |
|------|---------|---------------|
| SearchProducts | web-frontend | `http.method=GET`, `http.url=/api/v2/search/ai`, `search.query=...` |
| GET /api/v2/search/ai | api-gateway | `http.status_code=200` |
| ValidateToken | auth-service | standard auth span |
| GenerateQueryEmbedding | embedding-service | `gen_ai.operation.name=embedding` |
| `embedding text-embedding-3-small` | llm-gateway | Use `embeddingSpan()`: `gen_ai.system=openai`, `gen_ai.request.model=text-embedding-3-small`, `gen_ai.usage.input_tokens=12-48`, `gen_ai.embeddings.dimension.count=1536` |
| `retrieve product-embeddings` | vector-db-service | `gen_ai.operation.name=retrieve`, `gen_ai.data_source.id=product-embeddings`, `db.system=qdrant`, `db.operation=search`, `vector_db.top_k=20`, `vector_db.results_returned=15-20`, `vector_db.similarity_metric=cosine` |
| `chat gpt-4o-mini` (rerank) | llm-gateway | Use `chatSpan()`: `gen_ai.system=openai`, `gen_ai.request.model=gpt-4o-mini`, `gen_ai.usage.input_tokens=800-2400`, `gen_ai.usage.output_tokens=150-400`, `gen_ai.request.max_tokens=512`, `gen_ai.response.finish_reasons=["stop"]` |
| Redis SET search-cache | cache-service | `db.system=redis`, `db.operation=SET`, `db.redis.ttl_seconds=300` |
| TrackEvent semantic_search.complete | analytics-service | `analytics.event_type=semantic_search.complete` |

**Error injection:**
- `errorChance(0.05)`: llm-gateway returns 429 (rate limit) -> fallback to Elasticsearch text search (creates alternative trace shape)
- `errorChance(0.03)`: vector-db-service timeout -> serve degraded results
- `errorChance(0.02)`: LLM `finish_reason=length` (truncated) -> logged as warning span event

**Timing (sleep ranges in ms):**
- Embedding generation: 80-250ms
- Vector search: 15-60ms
- LLM reranking: 400-1800ms (dominates trace)
- Cache write: 1-3ms

---

### AI-02: AI Chatbot with Tool Use

**Priority: 2** -- Establishes agent planning loop, tool call pattern, function calling.

**Why this matters for IAPM:** This is the pattern that Langfuse and LangSmith are most known for visualizing. The plan->tool->synthesize flow with `finish_reason=tool_calls` is the signature of agentic AI. IAPM shows it in 3D with the tool calls fanning out to real backend services.

```
Services: web-frontend -> api-gateway -> auth-service -> ai-agent-service ->
          llm-gateway [plan] -> {order-service, search-service, shipping-service} [parallel] ->
          llm-gateway [synthesize] -> cache-service
Shape: Double bowtie (plan -> fan-out -> gather -> synthesize)
Spans: 18-22
```

**Key spans:**

| Span | Service | Key Attributes |
|------|---------|---------------|
| ProcessChatMessage | ai-agent-service | `agent.session_id=sess_...`, `agent.turn_number=1-5`, `agent.intent=order_status\|product_search\|general` |
| PlanToolUse | llm-gateway | `gen_ai.request.model=gpt-4o`, `gen_ai.usage.input_tokens=200-800`, `gen_ai.usage.output_tokens=50-200`, `gen_ai.response.finish_reason=tool_calls`, `gen_ai.request.tool_count=8` |
| GetOrderStatus | order-service | `order.id=...`, `rpc.system=grpc` |
| ProductSearch | search-service | `db.system=elasticsearch`, `search.query=...` |
| GetTrackingInfo | shipping-service | `shipping.tracking_number=...` |
| SynthesizeResponse | llm-gateway | `gen_ai.request.model=gpt-4o`, `gen_ai.usage.input_tokens=400-1600`, `gen_ai.usage.output_tokens=100-400`, `gen_ai.response.finish_reason=stop` |
| Redis SET conversation:session | cache-service | `db.redis.ttl_seconds=1800` |

**Error injection:**
- `errorChance(0.04)`: Hallucinated tool call -> `agent.error=hallucinated_tool`, fallback response
- `errorChance(0.03)`: Token budget exceeded -> `agent.error=token_budget_exceeded`
- `errorChance(0.02)`: LLM timeout at plan step -> immediate "I'm having trouble" response

---

### AI-10: AI Content Moderation Pipeline

**Priority: 3** -- Introduces safety filter branch (approve/flag/block), parallel LLM classifiers.

**Why this matters for IAPM:** Galileo and Portkey market guardrails as their differentiator. This scenario generates `moderation.*` and `guardrail.*` attributes that prove IAPM can visualize guardrail decisions in context.

```
Services: web-frontend -> api-gateway -> auth-service -> content-moderation-service ->
          llm-gateway [safety] || llm-gateway [spam] -> {product-service | notification-service | [block]}
Shape: Linear with parallel LLM calls + 3-way branch
Spans: 12-16
```

**Key attributes for guardrail visualization:**

```go
// On content-moderation-service spans
attribute.String("moderation.content_type", "review_text"),
attribute.Int("moderation.text_length", 50+rand.Intn(1950)),
attribute.String("moderation.decision", decision),     // "approve" | "flag" | "block"
attribute.String("moderation.policy_version", "v2.3"),
attribute.Float64("moderation.safety_score", score),    // 0.0-1.0
attribute.String("moderation.flagged_categories", cats), // e.g. "hate,spam"
attribute.Bool("guardrail.triggered", score > 0.7),
attribute.String("guardrail.action", action),           // "pass" | "flag_for_review" | "block"
attribute.String("guardrail.name", "content_safety_v2"),
```

---

### AI-03 through AI-12: Summary Table

| ID | Scenario | Priority | Spans | Key LLM APM Signal |
|----|----------|----------|-------|---------------------|
| AI-03 | Content Generation | 6 | 12-15 | `gen_ai.request.temperature=0.7`, content safety filter |
| AI-04 | Dynamic Pricing Agent | 8 | 14-18 | Headless agent, feature store, batch price updates |
| AI-05 | Inventory Reorder Agent | 10 | 16-20 | Demand forecast, autonomous purchase orders |
| AI-06 | AI Customer Support | 5 | 16-20 | Sentiment classification, intent detection, escalation |
| AI-07 | Embedding Pipeline | 7 | 25-40 | Batch embedding, high fan-out, vector upsert throughput |
| AI-08 | Fraud with Explainability | 9 | 10-12 | SHAP-style feature attribution via LLM |
| AI-09 | Model Retraining | 11 | 14-18 | ML training spans, model registry, quality gate |
| AI-11 | Multi-Step Agent | 4 | 28-40 | Iterative plan->act->reflect loop, highest topology value |
| AI-12 | Conversational Commerce | 12 | 10-14/turn | Multi-turn session, growing context tokens |

---

## L1: Traditional Scenario Summary

| ID | Scenario | Priority | Spans | Key Pattern |
|----|----------|----------|-------|-------------|
| T-01 | User Registration | 5 | 12-14 | Email verification token, duplicate detection |
| T-02 | Product Review | 8 | 10-12 | Optimistic write + async moderation |
| T-03 | Return/Refund | 3 | 16-18 | Parallel refund + restock, reverse money flow |
| T-04 | Wishlist + Price Alert | 11 | 8-10 | Write-through cache, async monitoring |
| T-05 | Coupon Application | 6 | 11-13 | Cart recalculation chain, validation branch |
| T-06 | Gift Card | 10 | 10-12 | Payment splitting, balance check |
| T-07 | Subscription Mgmt | 12 | 12-15 | Stripe subscription, renewal webhook |
| T-08 | A/B Test Exposure | 7 | 8-10 | Feature flag variant, sticky session |
| T-09 | Rate Limiting | 9 | 4-6 | Redis sliding window, 429, early termination |
| T-10 | Admin Product CRUD | 13 | 14-16 | Write-amplification: cache + search reindex |
| T-11 | Order History | 14 | 8-10 | Keyset pagination, cursor-based |
| T-12 | Support Ticket | 15 | 10-12 | Cross-domain trace, SLA assignment |
| T-13 | Multi-Currency | 16 | 9-11 | External FX API, cache hit ratio |

---

## L2: Implementation Order and File Structure

### Phase 0: Prerequisites (before any new scenarios)

1. **Update semconv import** from v1.26.0 to v1.27+ for `gen_ai.*` attribute constants
2. **Add 8 new services** to pod list (16 new pods)
3. **Add exception arrays**: `llmExceptions`, `vectorDBExceptions`, `agentExceptions`, `moderationExceptions`
4. **Add `llmSpan()` helper** and `randomHex()` utility

### Phase 1: First 5 scenarios (before file split)

| Order | Scenario | Justification |
|-------|----------|---------------|
| 1 | AI-01: RAG Search | Establishes embedding + vector DB pattern |
| 2 | AI-02: Chatbot Tool Use | Establishes agent + tool call pattern |
| 3 | T-03: Return/Refund | Most-requested traditional gap |
| 4 | AI-11: Multi-Step Agent | Highest topology differentiation |
| 5 | AI-10: Content Moderation | Establishes guardrail pattern |

### Phase 2: File split refactor

Split into `scenarios/traditional/`, `scenarios/ai/`, `scenarios/common/` as detailed in e-001.

### Phase 3: Remaining scenarios (after split)

Implement remaining 20 scenarios in priority order from tables above.

### New CLI Flags

```go
noAIBackends := flag.Bool("no-ai-backends", false, "disable all LLM/AI backends (AI spans emit errors)")
aiOnly := flag.Bool("ai-only", false, "only run AI agentic scenarios")
```

### Updated Scenario Registration

```go
type namedScenario struct {
    name     string
    fn       func(context.Context)
    isError  bool   // excluded when -errors=0
    isAI     bool   // excluded when -ai-only=false (future), included by default
    category string // "traditional" | "ai" | "chaos"
}
```

---

## L2: Market Positioning Impact

### What This Enables for IAPM Demos

With these 25 new scenarios, IAPM can demonstrate:

| Demo Scenario | What Competitor Shows | What IAPM Shows |
|--------------|----------------------|-----------------|
| "Show me LLM token costs" | Langfuse: 2D table of token counts | IAPM: Token costs on spans within 3D service topology -- see WHERE in your architecture costs accumulate |
| "Show me agent tool calls" | LangSmith: Flat trace waterfall | IAPM: 3D bowtie showing plan->fan-out->gather->synthesize across real services |
| "Show me guardrail violations" | Portkey: Dashboard counter | IAPM: Guardrail trigger visible as branch point in 3D trace, showing downstream impact |
| "Show me embedding pipeline" | Arize: Latency chart | IAPM: 3D fan-out of batch embedding chunks, with token throughput on each |
| "Correlate LLM latency with user experience" | Nobody (gap) | IAPM: Full trace from browser click through LLM call to response -- unified APM + AI |
| "Show me multi-step agent execution" | LangSmith: Nested steps | IAPM: Iterative loop topology in 3D -- each plan->act->reflect cycle visible as repeating structure |

### The "No Tool Does Both" Proof Point

The trace generator now produces both:
1. Traditional distributed traces (order flows, saga compensation, health checks) that require APM
2. AI agentic traces (RAG, chatbots, agents) that require LLM observability

**No single LLM tool (Langfuse, LangSmith, Helicone, Arize, Traceloop, Portkey, Galileo) can visualize both.** IAPM can. The trace generator proves it.

---

## References

| Source | Used For |
|--------|----------|
| docs/analysis/ps-tracegen-001-e-001-scenario-expansion.md | Scenario definitions, pod assignments, architecture recommendations |
| SP-023 tools-analysis.md | LLM APM market features, OTel GenAI conventions, competitive gap |
| [OTel GenAI Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/) | Attribute names, span conventions, operation types |
| [OTel GenAI Attribute Registry](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/) | Complete gen_ai.* attribute list with types |
| [OTel GenAI Agent Spans](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-agent-spans/) | Agent span conventions: create_agent, invoke_agent, execute_tool |
| [MS Agent Framework Observability](https://learn.microsoft.com/en-us/agent-framework/tutorials/agents/enable-observability) | Production span shapes: invoke_agent, chat, execute_tool |
| [MS Semantic Kernel Observability](https://learn.microsoft.com/en-us/semantic-kernel/concepts/enterprise-readiness/observability/) | Activity sources, metrics, gen_ai attribute usage |

---

## PS Integration

```yaml
builder_output:
  ps_id: "ps-tracegen-001"
  entry_id: "e-002"
  type: "implementation_specification"
  artifact_path: "docs/analysis/ps-tracegen-001-e-002-implementation-spec.md"
  depends_on: ["e-001"]
  key_decisions:
    - "Every llm-gateway span MUST emit gen_ai.usage.input_tokens + output_tokens (LLM APM parity)"
    - "chatSpan(), embeddingSpan(), agentSpan(), toolSpan() helpers ensure attribute consistency"
    - "Span names follow OTel convention: chat {model}, embedding {model}, invoke_agent {name}, execute_tool {name}"
    - "Emit both gen_ai.system AND gen_ai.provider.name for backward + forward compat"
    - "gen_ai.response.finish_reasons is a string ARRAY, not singular"
    - "8 new services, 16 new pods, total 59 pods"
    - "Phase 1: 5 scenarios before file split; Phase 2: split; Phase 3: remaining 20"
    - "New -no-ai-backends and -ai-only flags for scenario filtering"
  market_alignment: "Attributes match what Langfuse, LangSmith, Helicone, Arize, Traceloop, Portkey, and Galileo all track"
  ms_alignment: "Span shapes match Microsoft Semantic Kernel and Agent Framework OTel output (invoke_agent, chat, execute_tool)"
```
