package main

import (
	"context"
	"fmt"
	"math/rand"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	logapi "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

// ─── AI-01: Semantic Search with RAG ──────────────────────────────────────────
// web-frontend -> api-gateway -> auth-service -> embedding-service ->
// llm-gateway [embed] -> vector-db-service -> llm-gateway [rerank] ->
// cache-service -> analytics-service
func ragSearchFlow(ctx context.Context) {
	query := randomSearchTerm()

	ctx, ui := tracer("web-frontend").Start(ctx, "SearchProducts",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.url", "/api/v2/search/ai"),
			attribute.String("browser.page", "/search"),
			attribute.String("search.query", query),
		),
	)
	defer ui.End()
	sleep(2, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "GET /api/v2/search/ai",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.route", "/api/v2/search/ai"),
			attribute.Int("http.status_code", 200),
			attribute.String("search.query", query),
		),
	)
	defer gateway.End()
	sleep(3, 10)

	// Auth check
	_, auth := tracer("auth-service").Start(ctx, "ValidateToken",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateToken"),
		),
	)
	sleep(3, 10)
	auth.End()

	// Generate query embedding
	embedModel := randomEmbeddingModel()
	emitLog(ctx, "embedding-service", logapi.SeverityInfo, "Generating query embedding",
		logapi.String("embedding.model", embedModel.name))
	_, embedSvc := tracer("embedding-service").Start(ctx, "GenerateQueryEmbedding",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("embedding.input_type", "query"),
			attribute.Int("embedding.text_length", len(query)),
		),
	)
	sleep(5, 15)

	inputTokens := 12 + rand.Intn(36)
	_, embedLLM := embeddingSpan(ctx, embedModel, inputTokens, 1536)
	sleep(80, 250)

	// Error: LLM rate limit -> fallback to text search
	if errorChance(0.05) {
		exc := randomException(llmExceptions)
		recordException(ctx, "llm-gateway", embedLLM, exc.excType, exc.message, exc.stacktrace)
		embedLLM.End()
		embedSvc.End()

		// Fallback to Elasticsearch text search
		emitLog(ctx, "embedding-service", logapi.SeverityWarn, "LLM rate limited, falling back to text search")
		_, esFallback := tracer("search-service").Start(ctx, "Elasticsearch Query (fallback)",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("db.system", "elasticsearch"),
				attribute.String("db.operation", "search"),
				attribute.Bool("search.fallback", true),
				attribute.String("search.fallback_reason", "llm_rate_limit"),
				attribute.Int("search.results_count", rand.Intn(30)),
			),
		)
		sleep(30, 120)
		esFallback.End()
		return
	}
	embedLLM.End()
	embedSvc.End()

	// Vector similarity search
	resultsReturned := 15 + rand.Intn(6)
	_, vecSearch := tracer("vector-db-service").Start(ctx, "retrieve product-embeddings",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "retrieve"),
			attribute.String("gen_ai.data_source.id", "product-embeddings"),
			attribute.String("db.system", "qdrant"),
			attribute.String("db.operation", "search"),
			attribute.Int("vector_db.top_k", 20),
			attribute.Int("vector_db.results_returned", resultsReturned),
			attribute.String("vector_db.similarity_metric", "cosine"),
		),
	)
	sleep(15, 60)
	if errorChance(0.03) {
		exc := randomException(vectorDBExceptions)
		recordException(ctx, "vector-db-service", vecSearch, exc.excType, exc.message, exc.stacktrace)
	}
	vecSearch.End()

	// LLM reranking
	rerankInputTokens := 800 + rand.Intn(1600)
	rerankOutputTokens := 150 + rand.Intn(250)
	_, rerank := chatSpan(ctx, randomLightChatModel(), rerankInputTokens, rerankOutputTokens)
	rerank.SetAttributes(
		attribute.Float64("gen_ai.request.temperature", 0.0),
		attribute.Int("gen_ai.request.max_tokens", 512),
	)
	sleep(400, 1800)
	// Occasional truncation
	if errorChance(0.02) {
		rerank.SetAttributes(attribute.StringSlice("gen_ai.response.finish_reasons", []string{"length"}))
		rerank.AddEvent("warning", trace.WithAttributes(
			attribute.String("message", "LLM reranking response truncated"),
		))
	}
	rerank.End()

	// Cache result
	_, cacheSet := tracer("cache-service").Start(ctx, "Redis SET search-cache",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "SET"),
			attribute.Int("db.redis.ttl_seconds", 300),
		),
	)
	sleep(1, 3)
	cacheSet.End()

	// Analytics
	_, analytics := tracer("analytics-service").Start(ctx, "TrackEvent semantic_search.complete",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", "analytics.events"),
			attribute.String("analytics.event_type", "semantic_search.complete"),
			attribute.Int("search.results_count", resultsReturned),
		),
	)
	sleep(2, 5)
	analytics.End()

	if consumersEnabled {
		sleep(20, 80)
		_, analyticsConsumer := tracer("analytics-service").Start(ctx, "ProcessEvent semantic_search.complete",
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("messaging.system", "kafka"),
				attribute.String("messaging.operation", "receive"),
				attribute.String("messaging.destination", "analytics.events"),
				attribute.String("analytics.event_type", "semantic_search.complete"),
			),
		)
		sleep(5, 15)
		analyticsConsumer.End()
	}
}

// ─── AI-02: AI Chatbot with Tool Use ──────────────────────────────────────────
// web-frontend -> api-gateway -> auth-service -> ai-agent-service ->
// llm-gateway [plan] -> {order-service, search-service, shipping-service} [parallel] ->
// llm-gateway [synthesize] -> cache-service
func aiChatbotFlow(ctx context.Context) {
	sessionID := randomSessionID()
	userID := randomUserID()

	ctx, ui := tracer("web-frontend").Start(ctx, "SendChatMessage",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/chat"),
			attribute.String("browser.page", "/support/chat"),
			attribute.String("user.session_id", userID),
		),
	)
	defer ui.End()
	sleep(2, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/chat",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/chat"),
			attribute.Int("http.status_code", 200),
		),
	)
	defer gateway.End()
	sleep(3, 8)

	// Auth
	_, auth := tracer("auth-service").Start(ctx, "ValidateToken",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateToken"),
		),
	)
	sleep(3, 8)
	auth.End()

	// Agent invocation
	emitLog(ctx, "ai-agent-service", logapi.SeverityInfo, "Invoking CustomerSupportAgent",
		logapi.String("gen_ai.agent.id", "csa-001"), logapi.String("session.id", sessionID))
	ctx, agent := agentSpan(ctx, "CustomerSupportAgent", "csa-001", "Handles customer inquiries about orders and products", sessionID)
	defer agent.End()
	sleep(5, 15)

	// Planning step — LLM decides which tools to call
	chatModel := randomChatModel()
	planInputTokens := 200 + rand.Intn(600)
	planOutputTokens := 50 + rand.Intn(150)
	_, plan := chatSpan(ctx, chatModel, planInputTokens, planOutputTokens, "tool_calls")
	plan.SetAttributes(
		attribute.Int("gen_ai.request.max_tokens", 1024),
		attribute.Float64("gen_ai.request.temperature", 0.1),
	)
	sleep(300, 800)

	// Error: hallucinated tool call
	if errorChance(0.04) {
		exc := randomException(agentExceptions)
		recordException(ctx, "ai-agent-service", plan, exc.excType, exc.message, exc.stacktrace)
		plan.End()
		// Fallback response
		_, fallback := chatSpan(ctx, chatModel, 100, 80)
		fallback.SetAttributes(attribute.Int("gen_ai.request.max_tokens", 256))
		sleep(200, 500)
		fallback.End()
		return
	}
	plan.End()

	// Fan-out tool calls in parallel
	tools := make(chan struct{}, 3)

	// Tool 1: Get order status
	go func() {
		toolCtx, tool := toolSpan(ctx, "get_order_status", "Get the status of a customer order")
		sleep(5, 15)
		_, orderLookup := tracer("order-service").Start(toolCtx, "GetOrderByID",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("order.id", randomOrderID()),
				attribute.String("rpc.system", "grpc"),
			),
		)
		sleep(10, 30)
		orderLookup.End()
		tool.End()
		tools <- struct{}{}
	}()

	// Tool 2: Search products
	go func() {
		toolCtx, tool := toolSpan(ctx, "search_products", "Search product catalog by query")
		sleep(5, 10)
		_, search := tracer("search-service").Start(toolCtx, "Elasticsearch Query",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("db.system", "elasticsearch"),
				attribute.String("db.operation", "search"),
				attribute.Int("search.results_count", rand.Intn(20)),
			),
		)
		sleep(20, 60)
		search.End()
		tool.End()
		tools <- struct{}{}
	}()

	// Tool 3: Get tracking info
	go func() {
		toolCtx, tool := toolSpan(ctx, "get_tracking", "Get shipping tracking information")
		sleep(5, 10)
		_, tracking := tracer("shipping-service").Start(toolCtx, "GetTrackingInfo",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("shipping.tracking_number", fmt.Sprintf("1Z%s", randomHex(16))),
				attribute.String("rpc.system", "grpc"),
			),
		)
		sleep(15, 40)
		tracking.End()
		tool.End()
		tools <- struct{}{}
	}()

	for i := 0; i < 3; i++ {
		<-tools
	}

	// Synthesize response with tool results
	synthInputTokens := 400 + rand.Intn(1200)
	synthOutputTokens := 100 + rand.Intn(300)
	_, synth := chatSpan(ctx, chatModel, synthInputTokens, synthOutputTokens)
	synth.SetAttributes(
		attribute.Int("gen_ai.request.max_tokens", 1024),
		attribute.Float64("gen_ai.request.temperature", 0.3),
	)
	sleep(500, 1500)
	synth.End()

	// Update agent span with total token usage
	agent.SetAttributes(
		attribute.Int("gen_ai.usage.input_tokens", planInputTokens+synthInputTokens),
		attribute.Int("gen_ai.usage.output_tokens", planOutputTokens+synthOutputTokens),
	)

	// Cache conversation
	_, cacheSet := tracer("cache-service").Start(ctx, "Redis SET conversation:session",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "SET"),
			attribute.String("db.redis.key", "conversation:"+sessionID),
			attribute.Int("db.redis.ttl_seconds", 1800),
		),
	)
	sleep(1, 3)
	cacheSet.End()
}

// ─── AI-10: Content Moderation Pipeline ───────────────────────────────────────
// web-frontend -> api-gateway -> auth-service -> content-moderation-service ->
// llm-gateway [safety] || llm-gateway [spam] -> {product-service | notification-service | [block]}
func contentModerationFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "SubmitReview",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/reviews"),
			attribute.String("browser.page", "/product/review"),
		),
	)
	defer ui.End()
	sleep(2, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/reviews",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/reviews"),
		),
	)
	defer gateway.End()
	sleep(3, 8)

	// Auth
	_, auth := tracer("auth-service").Start(ctx, "ValidateToken",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateToken"),
		),
	)
	sleep(3, 8)
	auth.End()

	// Content moderation pipeline
	emitLog(ctx, "content-moderation-service", logapi.SeverityInfo, "Starting content moderation pipeline")
	textLength := 50 + rand.Intn(1950)
	safetyScore := rand.Float64()
	spamScore := rand.Float64() * 0.5

	// Determine decision based on scores
	decision := "approve"
	guardrailAction := "pass"
	if safetyScore > 0.7 || errorChance(0.08) {
		decision = "block"
		guardrailAction = "block"
		safetyScore = 0.7 + rand.Float64()*0.3
	} else if safetyScore > 0.4 {
		decision = "flag"
		guardrailAction = "flag_for_review"
	}

	ctx, moderation := tracer("content-moderation-service").Start(ctx, "ModerateContent",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("moderation.content_type", "review_text"),
			attribute.Int("moderation.text_length", textLength),
			attribute.String("moderation.decision", decision),
			attribute.String("moderation.policy_version", "v2.3"),
			attribute.Float64("moderation.safety_score", safetyScore),
			attribute.Bool("guardrail.triggered", safetyScore > 0.7),
			attribute.String("guardrail.action", guardrailAction),
			attribute.String("guardrail.name", "content_safety_v2"),
		),
	)
	defer moderation.End()
	sleep(5, 15)

	// Parallel LLM classifiers
	classifiers := make(chan struct{}, 2)

	// Safety classifier
	go func() {
		_, safety := chatSpan(ctx, randomLightChatModel(), 100+rand.Intn(200), 30+rand.Intn(50))
		safety.SetAttributes(
			attribute.Float64("gen_ai.request.temperature", 0.0),
			attribute.Int("gen_ai.request.max_tokens", 64),
			attribute.String("gen_ai.evaluation.name", "content_safety"),
			attribute.Float64("gen_ai.evaluation.score.value", safetyScore),
		)
		sleep(200, 600)
		safety.End()
		classifiers <- struct{}{}
	}()

	// Spam classifier
	go func() {
		_, spam := chatSpan(ctx, randomLightChatModel(), 80+rand.Intn(150), 20+rand.Intn(30))
		spam.SetAttributes(
			attribute.Float64("gen_ai.request.temperature", 0.0),
			attribute.Int("gen_ai.request.max_tokens", 32),
			attribute.String("gen_ai.evaluation.name", "spam_detection"),
			attribute.Float64("gen_ai.evaluation.score.value", spamScore),
		)
		sleep(150, 500)
		spam.End()
		classifiers <- struct{}{}
	}()

	for i := 0; i < 2; i++ {
		<-classifiers
	}

	// Branch based on decision
	switch decision {
	case "approve":
		// Publish review
		_, publish := tracer("product-service").Start(ctx, "PublishReview",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("review.status", "published"),
				attribute.String("product.id", fmt.Sprintf("prod_%06d", rand.Intn(999999))),
			),
		)
		sleep(10, 30)
		publish.End()

		gateway.SetAttributes(attribute.Int("http.status_code", 201))

	case "flag":
		emitLog(ctx, "content-moderation-service", logapi.SeverityWarn, "Content flagged for human review")
		// Queue for human review
		_, queue := tracer("notification-service").Start(ctx, "QueueForReview",
			trace.WithSpanKind(trace.SpanKindProducer),
			trace.WithAttributes(
				attribute.String("messaging.system", "rabbitmq"),
				attribute.String("messaging.operation", "publish"),
				attribute.String("messaging.destination", "moderation.review_queue"),
				attribute.String("review.status", "flagged"),
			),
		)
		sleep(3, 8)
		queue.End()

		if consumersEnabled {
			sleep(20, 80)
			_, reviewer := tracer("notification-service").Start(ctx, "ProcessReview flagged_content",
				trace.WithSpanKind(trace.SpanKindConsumer),
				trace.WithAttributes(
					attribute.String("messaging.system", "rabbitmq"),
					attribute.String("messaging.operation", "receive"),
					attribute.String("messaging.destination", "moderation.review_queue"),
					attribute.String("review.status", "pending_human_review"),
				),
			)
			sleep(5, 15)
			reviewer.End()
		}

		gateway.SetAttributes(attribute.Int("http.status_code", 202))

	case "block":
		// Content blocked
		emitLog(ctx, "content-moderation-service", logapi.SeverityWarn, "Content blocked by moderation policy")
		exc := randomException(moderationExceptions)
		recordException(ctx, "content-moderation-service", moderation, exc.excType, exc.message, exc.stacktrace)

		gateway.SetAttributes(attribute.Int("http.status_code", 422))
		gateway.SetStatus(codes.Error, "content blocked by moderation")
	}
}

// ─── AI-11: Multi-Step Agent ──────────────────────────────────────────────────
// api-gateway -> ai-agent-service -> [plan -> act -> reflect] x 3-5 iterations
// Each iteration: llm-gateway [plan] -> execute_tool -> llm-gateway [reflect]
func multiStepAgentFlow(ctx context.Context) {
	sessionID := randomSessionID()

	ctx, ui := tracer("web-frontend").Start(ctx, "SubmitAgentTask",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/agent/task"),
			attribute.String("browser.page", "/agent/task"),
		),
	)
	defer ui.End()
	sleep(2, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/agent/task",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/agent/task"),
			attribute.Int("http.status_code", 200),
		),
	)
	defer gateway.End()
	sleep(3, 8)

	// Auth
	_, auth := tracer("auth-service").Start(ctx, "ValidateToken",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateToken"),
		),
	)
	sleep(3, 8)
	auth.End()

	// Agent invocation
	ctx, agent := agentSpan(ctx, "ResearchAgent", "ra-001", "Multi-step research agent that gathers data and produces reports", sessionID)
	defer agent.End()

	totalInputTokens := 0
	totalOutputTokens := 0
	iterations := 3 + rand.Intn(3) // 3-5 iterations

	toolRegistry := []struct{ name, desc string }{
		{"search_products", "Search product catalog"},
		{"get_order", "Get order details by ID"},
		{"get_product", "Get product details"},
		{"check_inventory", "Check product inventory levels"},
		{"get_user", "Get user profile"},
		{"search_reviews", "Search product reviews"},
	}

	agentModel := randomChatModel() // agent uses one model for entire session
	for i := 0; i < iterations; i++ {
		iterCtx := ctx
		emitLog(iterCtx, "ai-agent-service", logapi.SeverityInfo, fmt.Sprintf("Agent iteration %d/%d: planning", i+1, iterations),
			logapi.String("agent.phase", "plan"))

		// Plan step
		planInput := 200 + i*150 + rand.Intn(300) // grows with context
		planOutput := 50 + rand.Intn(100)
		totalInputTokens += planInput
		totalOutputTokens += planOutput

		_, plan := chatSpan(iterCtx, agentModel, planInput, planOutput, "tool_calls")
		plan.SetAttributes(
			attribute.Int("gen_ai.request.max_tokens", 1024),
			attribute.Float64("gen_ai.request.temperature", 0.2),
			attribute.Int("agent.iteration", i+1),
			attribute.String("agent.phase", "plan"),
		)
		sleep(300, 800)

		// Error: token budget exceeded
		if errorChance(0.03) && i >= 2 {
			exc := agentExceptions[2] // TokenBudgetExceeded
			recordException(iterCtx, "ai-agent-service", plan, exc.excType, exc.message, exc.stacktrace)
			plan.End()
			agent.SetStatus(codes.Error, "token budget exceeded")
			break
		}
		plan.End()

		// Act step — execute selected tool
		tool := toolRegistry[rand.Intn(len(toolRegistry))]
		toolCtx, toolExec := toolSpan(iterCtx, tool.name, tool.desc)
		sleep(10, 30)

		// Tool calls a real backend service
		switch tool.name {
		case "search_products", "search_reviews":
			_, search := tracer("search-service").Start(toolCtx, "Elasticsearch Query",
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("db.system", "elasticsearch"),
					attribute.String("db.operation", "search"),
					attribute.Int("search.results_count", rand.Intn(30)),
				),
			)
			sleep(20, 60)
			search.End()
		case "get_order":
			_, order := tracer("order-service").Start(toolCtx, "GetOrderByID",
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("order.id", randomOrderID()),
					attribute.String("rpc.system", "grpc"),
				),
			)
			sleep(10, 30)
			order.End()
		case "get_product":
			_, product := tracer("product-service").Start(toolCtx, "GetProductByID",
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("product.id", fmt.Sprintf("prod_%06d", rand.Intn(999999))),
					attribute.String("rpc.system", "grpc"),
				),
			)
			sleep(8, 25)
			product.End()
		case "check_inventory":
			_, inv := tracer("inventory-service").Start(toolCtx, "CheckStock",
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("rpc.system", "grpc"),
					attribute.Int("inventory.quantity", rand.Intn(500)),
				),
			)
			sleep(10, 25)
			inv.End()
		case "get_user":
			_, user := tracer("user-service").Start(toolCtx, "GetUserProfile",
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("user.id", randomUserID()),
					attribute.String("rpc.system", "grpc"),
				),
			)
			sleep(8, 20)
			user.End()
		}
		toolExec.End()

		// Reflect step — LLM evaluates tool results and decides next step
		reflectInput := 300 + i*200 + rand.Intn(400)
		reflectOutput := 80 + rand.Intn(120)
		totalInputTokens += reflectInput
		totalOutputTokens += reflectOutput

		finishReason := "tool_calls"
		if i == iterations-1 {
			finishReason = "stop" // final iteration produces answer
		}
		_, reflect := chatSpan(iterCtx, agentModel, reflectInput, reflectOutput, finishReason)
		reflect.SetAttributes(
			attribute.Float64("gen_ai.request.temperature", 0.2),
			attribute.Int("agent.iteration", i+1),
			attribute.String("agent.phase", "reflect"),
		)
		sleep(400, 1200)
		reflect.End()
	}

	// Set total token usage on agent span
	emitLog(ctx, "ai-agent-service", logapi.SeverityInfo,
		fmt.Sprintf("Agent completed %d iterations, total tokens: %d in / %d out", iterations, totalInputTokens, totalOutputTokens))
	agent.SetAttributes(
		attribute.Int("gen_ai.usage.input_tokens", totalInputTokens),
		attribute.Int("gen_ai.usage.output_tokens", totalOutputTokens),
	)

	// Cache agent result
	_, cacheSet := tracer("cache-service").Start(ctx, "Redis SET agent:result",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "SET"),
			attribute.Int("db.redis.ttl_seconds", 600),
		),
	)
	sleep(1, 3)
	cacheSet.End()

	// Analytics
	_, analytics := tracer("analytics-service").Start(ctx, "TrackEvent agent.task_complete",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", "analytics.events"),
			attribute.String("analytics.event_type", "agent.task_complete"),
			attribute.Int("agent.total_iterations", iterations),
			attribute.Int("agent.total_input_tokens", totalInputTokens),
			attribute.Int("agent.total_output_tokens", totalOutputTokens),
		),
	)
	sleep(2, 5)
	analytics.End()

	if consumersEnabled {
		sleep(20, 80)
		_, analyticsConsumer := tracer("analytics-service").Start(ctx, "ProcessEvent agent.task_complete",
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("messaging.system", "kafka"),
				attribute.String("messaging.operation", "receive"),
				attribute.String("messaging.destination", "analytics.events"),
				attribute.String("analytics.event_type", "agent.task_complete"),
			),
		)
		sleep(5, 15)
		analyticsConsumer.End()
	}
}

// ─── T-03: Return/Refund ─────────────────────────────────────────────────────
// web-frontend -> api-gateway -> auth-service -> order-service [lookup] ->
// parallel [payment-service refund, inventory-service restock] ->
// notification-service -> email-service -> analytics-service
func returnRefundFlow(ctx context.Context) {
	orderID := randomOrderID()

	ctx, ui := tracer("web-frontend").Start(ctx, "RequestReturn",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/returns"),
			attribute.String("browser.page", "/orders/return"),
			attribute.String("order.id", orderID),
		),
	)
	defer ui.End()
	sleep(2, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/returns",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/returns"),
			attribute.Int("http.status_code", 200),
		),
	)
	defer gateway.End()
	sleep(5, 15)

	// Auth
	_, auth := tracer("auth-service").Start(ctx, "ValidateToken",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateToken"),
		),
	)
	sleep(3, 8)
	auth.End()

	// Lookup order
	ctx2, orderLookup := tracer("order-service").Start(ctx, "GetOrderForReturn",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("order.id", orderID),
			attribute.String("order.status", "delivered"),
		),
	)
	sleep(10, 25)

	_, dbLookup := tracer("order-service").Start(ctx2, "SELECT orders WHERE id = $1",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.name", "orders_db"),
		),
	)
	sleep(5, 15)
	dbLookup.End()

	// Update order status
	_, dbUpdate := tracer("order-service").Start(ctx2, "UPDATE orders SET status = 'return_initiated'",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "UPDATE"),
			attribute.String("db.name", "orders_db"),
		),
	)
	sleep(5, 15)
	dbUpdate.End()
	orderLookup.End()

	// Parallel: refund + restock
	refundDone := make(chan struct{}, 2)
	refundAmount := 50 + rand.Float64()*500

	// Refund payment
	go func() {
		ctx3, refund := tracer("payment-service").Start(ctx, "ProcessRefund",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("order.id", orderID),
				attribute.String("payment.provider", "stripe"),
				attribute.Float64("payment.refund_amount", refundAmount),
			),
		)
		sleep(15, 40)

		_, stripeRefund := tracer("payment-service").Start(ctx3, "POST https://api.stripe.com/v1/refunds",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("http.method", "POST"),
				attribute.String("http.url", "https://api.stripe.com/v1/refunds"),
				attribute.String("peer.service", "stripe-api"),
				attribute.Int("http.status_code", 200),
			),
		)
		sleep(80, 250)
		stripeRefund.End()
		refund.End()
		refundDone <- struct{}{}
	}()

	// Restock inventory
	go func() {
		ctx3, restock := tracer("inventory-service").Start(ctx, "RestockItems",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("order.id", orderID),
				attribute.Int("inventory.items", 1+rand.Intn(5)),
			),
		)
		sleep(10, 30)

		_, dbRestock := tracer("inventory-service").Start(ctx3, "UPDATE inventory SET quantity = quantity + $1",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "postgresql"),
				attribute.String("db.operation", "UPDATE"),
				attribute.String("db.name", "inventory_db"),
			),
		)
		sleep(5, 15)
		dbRestock.End()

		// Invalidate cache
		_, cacheDel := tracer("cache-service").Start(ctx3, "Redis DEL inventory-cache",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "redis"),
				attribute.String("db.operation", "DEL"),
			),
		)
		sleep(1, 3)
		cacheDel.End()
		restock.End()
		refundDone <- struct{}{}
	}()

	for i := 0; i < 2; i++ {
		<-refundDone
	}

	// Notification
	ctx3, notify := tracer("notification-service").Start(ctx, "SendRefundConfirmation",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("notification.type", "refund_confirmation"),
			attribute.String("order.id", orderID),
		),
	)
	sleep(10, 25)

	_, email := tracer("email-service").Start(ctx3, "SendEmail",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("peer.service", "sendgrid"),
			attribute.String("email.template", "refund_confirmation"),
		),
	)
	sleep(25, 80)
	email.End()
	notify.End()

	// Analytics
	_, analytics := tracer("analytics-service").Start(ctx, "TrackEvent order.refunded",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", "analytics.events"),
			attribute.String("analytics.event_type", "order.refunded"),
			attribute.String("order.id", orderID),
			attribute.Float64("refund.amount", refundAmount),
		),
	)
	sleep(2, 5)
	analytics.End()

	if consumersEnabled {
		sleep(20, 80)
		_, analyticsConsumer := tracer("analytics-service").Start(ctx, "ProcessEvent order.refunded",
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("messaging.system", "kafka"),
				attribute.String("messaging.operation", "receive"),
				attribute.String("messaging.destination", "analytics.events"),
				attribute.String("analytics.event_type", "order.refunded"),
				attribute.String("order.id", orderID),
			),
		)
		sleep(5, 15)
		analyticsConsumer.End()
	}
}
