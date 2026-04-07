package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	logapi "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

// recordException adds an OTel exception event with realistic type, message, and stacktrace.
// Also emits a correlated ERROR log record for the service.
func recordException(ctx context.Context, svc string, span trace.Span, excType, message, stacktrace string) {
	span.AddEvent("exception", trace.WithAttributes(
		attribute.String("exception.type", excType),
		attribute.String("exception.message", message),
		attribute.String("exception.stacktrace", stacktrace),
	))
	span.SetStatus(codes.Error, message)
	emitLog(ctx, svc, logapi.SeverityError, message,
		logapi.String("exception.type", excType))
}

type exceptionInfo struct {
	excType    string
	message    string
	stacktrace string
}

var dbExceptions = []exceptionInfo{
	{
		"Npgsql.PostgresException",
		"23505: duplicate key value violates unique constraint \"orders_pkey\"",
		`Npgsql.PostgresException (0x80004005): 23505: duplicate key value violates unique constraint "orders_pkey"
   at Npgsql.Internal.NpgsqlConnector.<ReadMessage>g__ReadMessageLong|0(Boolean async)
   at Npgsql.NpgsqlDataReader.NextResult(Boolean async, Boolean isConsuming)
   at Npgsql.NpgsqlCommand.ExecuteReader(Boolean async, CommandBehavior behavior)
   at OrderService.Repositories.OrderRepository.CreateAsync(Order order) in /src/OrderService/Repositories/OrderRepository.cs:line 47`,
	},
	{
		"Npgsql.NpgsqlException",
		"Exception while reading from stream",
		`Npgsql.NpgsqlException (0x80004005): Exception while reading from stream
 ---> System.TimeoutException: Timeout during reading attempt
   at Npgsql.Internal.NpgsqlConnector.<ReadMessage>g__ReadMessageLong|0(Boolean async)
   at Npgsql.NpgsqlDataReader.NextResult(Boolean async, Boolean isConsuming)
   at InventoryService.Data.InventoryContext.QueryAsync(String sql) in /src/InventoryService/Data/InventoryContext.cs:line 83`,
	},
	{
		"System.InvalidOperationException",
		"Connection pool exhausted - max pool size (100) reached",
		`System.InvalidOperationException: Connection pool exhausted - max pool size (100) reached
   at Npgsql.PoolingDataSource.Get(NpgsqlTimeout timeout, Boolean async)
   at Npgsql.NpgsqlConnection.Open(Boolean async)
   at OrderService.Services.OrderLookupService.GetByIdAsync(Guid id) in /src/OrderService/Services/OrderLookupService.cs:line 31`,
	},
}

var httpExceptions = []exceptionInfo{
	{
		"System.Net.Http.HttpRequestException",
		"Response status code does not indicate success: 503 (Service Unavailable)",
		`System.Net.Http.HttpRequestException: Response status code does not indicate success: 503 (Service Unavailable)
   at System.Net.Http.HttpResponseMessage.EnsureSuccessStatusCode()
   at ApiGateway.Clients.PaymentClient.ChargeAsync(ChargeRequest request) in /src/ApiGateway/Clients/PaymentClient.cs:line 62
   at ApiGateway.Controllers.OrdersController.Post(CreateOrderRequest request) in /src/ApiGateway/Controllers/OrdersController.cs:line 44`,
	},
	{
		"System.Threading.Tasks.TaskCanceledException",
		"The request was canceled due to the configured HttpClient.Timeout of 30 seconds elapsing",
		`System.Threading.Tasks.TaskCanceledException: The request was canceled due to the configured HttpClient.Timeout of 30 seconds elapsing.
 ---> System.TimeoutException: A task was canceled.
   at System.Net.Http.HttpClient.<SendAsync>g__Core|0(HttpRequestMessage request)
   at NotificationService.Clients.SendGridClient.SendAsync(EmailMessage msg) in /src/NotificationService/Clients/SendGridClient.cs:line 89`,
	},
}

var cacheExceptions = []exceptionInfo{
	{
		"StackExchange.Redis.RedisConnectionException",
		"No connection is active/available to service this operation: PING",
		`StackExchange.Redis.RedisConnectionException: No connection is active/available to service this operation: PING; UnableToConnect on redis-primary:6379/Interactive, Initializing/NotStarted, last: NONE, origin: BeginConnectAsync, outstanding: 0
   at StackExchange.Redis.ConnectionMultiplexer.ExecuteSyncImpl[T](Message message)
   at CacheService.RedisProvider.GetAsync(String key) in /src/CacheService/RedisProvider.cs:line 42`,
	},
	{
		"StackExchange.Redis.RedisTimeoutException",
		"Timeout performing GET session:usr_004821, inst: 0, qu: 12, qs: 0, aw: False",
		`StackExchange.Redis.RedisTimeoutException: Timeout performing GET session:usr_004821 (5000ms), inst: 0, qu: 12, qs: 0, aw: False, bw: SpinningDown, rs: ReadAsync, ws: Idle, in: 0, last-in: 0, cur-in: 0
   at StackExchange.Redis.ConnectionMultiplexer.ExecuteSyncImpl[T](Message message)
   at CacheService.SessionStore.GetSessionAsync(String userId) in /src/CacheService/SessionStore.cs:line 58`,
	},
}

var searchExceptions = []exceptionInfo{
	{
		"Elasticsearch.Net.ElasticsearchClientException",
		"Request timed out after 30000ms",
		`Elasticsearch.Net.ElasticsearchClientException: Request timed out after 30000ms ---> System.OperationCanceledException: The operation was canceled.
   at Elasticsearch.Net.HttpConnection.RequestAsync[TResponse](RequestData requestData)
   at SearchService.Search.ProductSearchService.SearchAsync(SearchQuery query) in /src/SearchService/Search/ProductSearchService.cs:line 71`,
	},
}

var authExceptions = []exceptionInfo{
	{
		"System.Security.Authentication.AuthenticationException",
		"Invalid credentials for user user@example.com",
		`System.Security.Authentication.AuthenticationException: Invalid credentials for user user@example.com
   at AuthService.Services.AuthenticationService.ValidateAsync(LoginRequest request) in /src/AuthService/Services/AuthenticationService.cs:line 55
   at AuthService.Controllers.AuthController.Login(LoginRequest request) in /src/AuthService/Controllers/AuthController.cs:line 28`,
	},
	{
		"System.Security.SecurityException",
		"JWT token expired at 2026-03-03T23:45:00Z",
		`System.Security.SecurityException: JWT token expired at 2026-03-03T23:45:00Z
   at AuthService.Middleware.JwtValidator.ValidateToken(String token) in /src/AuthService/Middleware/JwtValidator.cs:line 39
   at AuthService.Middleware.AuthMiddleware.InvokeAsync(HttpContext context) in /src/AuthService/Middleware/AuthMiddleware.cs:line 22`,
	},
}

var paymentExceptions = []exceptionInfo{
	{
		"Stripe.StripeException",
		"Your card was declined. Your request was in test mode, but used a non test (live) card.",
		`Stripe.StripeException: Your card was declined. Your request was in test mode, but used a non test (live) card.
   at Stripe.StripeClient.RawRequestAsync(HttpMethod method, String path, String content)
   at PaymentService.Providers.StripeProvider.ChargeAsync(PaymentRequest request) in /src/PaymentService/Providers/StripeProvider.cs:line 94
   at PaymentService.Services.PaymentProcessor.ProcessAsync(Order order) in /src/PaymentService/Services/PaymentProcessor.cs:line 48`,
	},
	{
		"PaymentService.Exceptions.InsufficientFundsException",
		"Insufficient funds for charge of $847.32 on card ending 4242",
		`PaymentService.Exceptions.InsufficientFundsException: Insufficient funds for charge of $847.32 on card ending 4242
   at PaymentService.Providers.StripeProvider.ChargeAsync(PaymentRequest request) in /src/PaymentService/Providers/StripeProvider.cs:line 101
   at PaymentService.Services.PaymentProcessor.ProcessAsync(Order order) in /src/PaymentService/Services/PaymentProcessor.cs:line 48
   at PaymentService.Workers.PaymentWorker.HandleMessage(RabbitMQ.Message msg) in /src/PaymentService/Workers/PaymentWorker.cs:line 33`,
	},
}

// parseHeaders parses "key=value,key2=value2" into a map.
// Compatible with OTEL_EXPORTER_OTLP_HEADERS env var format.
func parseHeaders(s string) map[string]string {
	if s == "" {
		return nil
	}
	headers := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" {
			headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func randomException(exceptions []exceptionInfo) exceptionInfo {
	return exceptions[rand.Intn(len(exceptions))]
}

func sleep(minMs, maxMs int) {
	d := time.Duration(minMs+rand.Intn(maxMs-minMs+1)) * time.Millisecond
	time.Sleep(d)
}

func randomIP() string {
	return fmt.Sprintf("10.%d.%d.%d", rand.Intn(256), rand.Intn(256), rand.Intn(256))
}

func randomUserID() string {
	return fmt.Sprintf("usr_%06d", rand.Intn(999999))
}

func randomOrderID() string {
	return fmt.Sprintf("ord_%08d", rand.Intn(99999999))
}

func randomSearchTerm() string {
	terms := []string{"widget", "dashboard", "monitor", "alert rules", "trace analysis", "service health",
		"wireless keyboard", "USB-C hub", "laptop stand", "noise cancelling headphones", "ergonomic mouse"}
	return terms[rand.Intn(len(terms))]
}

func randomSessionID() string {
	return fmt.Sprintf("sess_%s", randomHex(12))
}

func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := cryptorand.Read(b); err != nil {
		// Fallback to math/rand if crypto/rand fails
		for i := range b {
			b[i] = byte(rand.Intn(256))
		}
	}
	return hex.EncodeToString(b)[:n]
}

// ─── GenAI Model Registry ────────────────────────────────────────────────────
// Current models across providers, randomized per span for realistic variety.

type modelInfo struct {
	name     string // model identifier
	system   string // gen_ai.system value
	endpoint string // API endpoint URL
	peer     string // peer.service value
}

// Chat/completion models — mix of providers and tiers
var chatModels = []modelInfo{
	{"gpt-5.4", "openai", "https://api.openai.com/v1/chat/completions", "openai-api"},
	{"gpt-5.4-mini", "openai", "https://api.openai.com/v1/chat/completions", "openai-api"},
	{"gpt-5.1", "openai", "https://api.openai.com/v1/chat/completions", "openai-api"},
	{"claude-sonnet-4-5", "anthropic", "https://api.anthropic.com/v1/messages", "anthropic-api"},
	{"claude-opus-4-5", "anthropic", "https://api.anthropic.com/v1/messages", "anthropic-api"},
	{"claude-haiku-4-5", "anthropic", "https://api.anthropic.com/v1/messages", "anthropic-api"},
	{"DeepSeek-V3.2", "deepseek", "https://api.deepseek.com/v1/chat/completions", "deepseek-api"},
	{"grok-4-fast-reasoning", "xai", "https://api.x.ai/v1/chat/completions", "xai-api"},
	{"Mistral-Large-3", "mistral", "https://api.mistral.ai/v1/chat/completions", "mistral-api"},
}

// Lightweight/fast models for tool planning, classification, moderation
var lightChatModels = []modelInfo{
	{"gpt-5.4-mini", "openai", "https://api.openai.com/v1/chat/completions", "openai-api"},
	{"claude-haiku-4-5", "anthropic", "https://api.anthropic.com/v1/messages", "anthropic-api"},
	{"gpt-5.1-codex", "openai", "https://api.openai.com/v1/chat/completions", "openai-api"},
	{"Mistral-Large-3", "mistral", "https://api.mistral.ai/v1/chat/completions", "mistral-api"},
}

// Embedding models
var embeddingModels = []modelInfo{
	{"embed-v4.0", "openai", "https://api.openai.com/v1/embeddings", "openai-api"},
	{"Cohere-rerank-v4.0-pro", "cohere", "https://api.cohere.com/v2/embed", "cohere-api"},
}

func randomChatModel() modelInfo       { return chatModels[rand.Intn(len(chatModels))] }
func randomLightChatModel() modelInfo  { return lightChatModels[rand.Intn(len(lightChatModels))] }
func randomEmbeddingModel() modelInfo  { return embeddingModels[rand.Intn(len(embeddingModels))] }

// ─── GenAI Helper Functions ───────────────────────────────────────────────────
// These produce spans matching Microsoft Semantic Kernel / Agent Framework OTel
// output and OTel GenAI Semantic Conventions v1.38.0.

// chatSpan creates a "chat {model}" span on llm-gateway with standard gen_ai attributes.
func chatSpan(ctx context.Context, m modelInfo, inputTokens, outputTokens int, finishReasons ...string) (context.Context, trace.Span) {
	if len(finishReasons) == 0 {
		finishReasons = []string{"stop"}
	}
	ctx, span := tracer("llm-gateway").Start(ctx, "chat "+m.name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.system", m.system),
			attribute.String("gen_ai.request.model", m.name),
			attribute.String("gen_ai.response.model", m.name),
			attribute.Int("gen_ai.usage.input_tokens", inputTokens),
			attribute.Int("gen_ai.usage.output_tokens", outputTokens),
			attribute.StringSlice("gen_ai.response.finish_reasons", finishReasons),
			attribute.String("gen_ai.response.id", "chatcmpl-"+randomHex(24)),
			attribute.String("http.method", "POST"),
			attribute.String("http.url", m.endpoint),
			attribute.String("peer.service", m.peer),
			attribute.Int("http.status_code", 200),
		),
	)
	return ctx, span
}

// embeddingSpan creates an "embedding {model}" span for text-to-vector operations.
func embeddingSpan(ctx context.Context, m modelInfo, inputTokens, dimensions int) (context.Context, trace.Span) {
	ctx, span := tracer("llm-gateway").Start(ctx, "embedding "+m.name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "embedding"),
			attribute.String("gen_ai.system", m.system),
			attribute.String("gen_ai.request.model", m.name),
			attribute.String("gen_ai.response.model", m.name),
			attribute.Int("gen_ai.usage.input_tokens", inputTokens),
			attribute.Int("gen_ai.embedding.dimension", dimensions),
			attribute.StringSlice("gen_ai.request.encoding_formats", []string{"float"}),
			attribute.String("gen_ai.response.id", "embd-"+randomHex(24)),
			attribute.String("http.method", "POST"),
			attribute.String("http.url", m.endpoint),
			attribute.String("peer.service", m.peer),
			attribute.Int("http.status_code", 200),
		),
	)
	return ctx, span
}

// agentSpan creates an "invoke_agent {name}" span matching MS Agent Framework output.
func agentSpan(ctx context.Context, agentName, agentID, description, sessionID string) (context.Context, trace.Span) {
	m := randomChatModel()
	ctx, span := tracer("ai-agent-service").Start(ctx, "invoke_agent "+agentName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.system", m.system),
			attribute.String("gen_ai.agent.id", agentID),
			attribute.String("gen_ai.agent.name", agentName),
			attribute.String("gen_ai.agent.description", description),
			attribute.String("gen_ai.agent.version", "1.0.0"),
			attribute.String("gen_ai.conversation.id", sessionID),
		),
	)
	return ctx, span
}

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

// ─── AI Exception Arrays ─────────────────────────────────────────────────────

var llmExceptions = []exceptionInfo{
	{
		"OpenAI.RateLimitError",
		"rate_limit_exceeded: Rate limit reached for gpt-5.4 in organization org-abc123 on tokens per min (TPM). Limit: 90000, Used: 89742, Requested: 1200.",
		`OpenAI.RateLimitError: rate_limit_exceeded: Rate limit reached for gpt-5.4 in organization org-abc123 on tokens per min (TPM). Limit: 90000, Used: 89742, Requested: 1200.
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
		"Request timed out after 30000ms waiting for response from gpt-5.4",
		`OpenAI.APITimeoutError: Request timed out after 30000ms waiting for response from gpt-5.4
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
		"LLM requested tool 'get_competitor_price' which is not in the tool registry",
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
		"Agent produced identical plan in consecutive iterations 3 and 4 -- loop detected",
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
		"PII detected in user content: email address pattern found at position 142",
		`Moderation.PIIDetected: PII detected in user content: email address pattern found at position 142
   at ContentModerationService.Classifiers.PIIDetector.ScanAsync(String content) in /src/ContentModerationService/Classifiers/PIIDetector.cs:line 67
   at ContentModerationService.Services.ModerationPipeline.ProcessAsync(ModerationRequest req) in /src/ContentModerationService/Services/ModerationPipeline.cs:line 41`,
	},
}
