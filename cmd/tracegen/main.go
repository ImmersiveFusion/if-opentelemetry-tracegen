package main

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

var tracerPool = map[string][]trace.Tracer{}
var providers []*sdktrace.TracerProvider

// noopTracer is returned for services not in the current complexity tier
var noopTracer = trace.NewNoopTracerProvider().Tracer("")

// tracer returns a random instance's tracer for the given service (multi-pod realism).
// Returns a noop tracer if the service is not in the current complexity tier.
func tracer(svc string) trace.Tracer {
	pool := tracerPool[svc]
	if len(pool) == 0 {
		return noopTracer
	}
	return pool[rand.Intn(len(pool))]
}

func newProvider(ctx context.Context, serviceName, endpoint, apiKey, instanceID, hostName string) *sdktrace.TracerProvider {
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithHeaders(map[string]string{"API-Key": apiKey}),
	}
	if insecureMode {
		opts = append(opts, otlptracegrpc.WithTLSCredentials(insecure.NewCredentials()))
	} else {
		opts = append(opts, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(&tls.Config{})))
	}
	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		log.Fatalf("failed to create exporter for %s: %v", serviceName, err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(serviceName),
			attribute.String("service.instance.id", instanceID),
			attribute.String("host.name", hostName),
		)),
	)
	tracerPool[serviceName] = append(tracerPool[serviceName], tp.Tracer(serviceName))
	providers = append(providers, tp)
	return tp
}

// Aggressiveness presets: tickMs, burstMin, burstMax
type levelConfig struct {
	tickMs   int
	burstMin int
	burstMax int
	label    string
}

var levels = map[int]levelConfig{
	1:  {500, 1, 1, "whisper (~2/s)"},
	2:  {400, 1, 1, "gentle (~3/s)"},
	3:  {300, 1, 1, "calm (~3/s)"},
	4:  {200, 1, 1, "moderate (~5/s)"},
	5:  {150, 1, 1, "steady (~7/s)"},
	6:  {100, 1, 2, "brisk (~15/s)"},
	7:  {70, 1, 2, "aggressive (~21/s)"},
	8:  {50, 2, 2, "intense (~40/s)"},
	9:  {30, 2, 3, "firehose (~83/s)"},
	10: {10, 3, 4, "SCREAM (~350/s)"},
}

// Global error multiplier — set by -errors flag, used by errorChance()
var errorMultiplier float64

// consumersEnabled — when false, all async consumers are dead (queue messages pile up)
var consumersEnabled bool

// insecureMode — when true, use plaintext gRPC (no TLS) for local backends
var insecureMode bool

// noAIBackends — when true, AI services are excluded (AI spans emit errors)
var noAIBackends bool

// aiOnly — when true, only AI agentic scenarios are run
var aiOnly bool

// complexity — controls how many services/pods/scenarios are active
var complexity string // "light", "normal", "heavy"

// errorChance scales a base error probability by the global error multiplier.
// Base rates are tuned for -errors=5 (moderate). At 0 = no errors, at 10 = ~2x base.
func errorChance(baseRate float64) bool {
	return rand.Float64() < baseRate*errorMultiplier
}

func main() {
	endpointFlag := flag.String("endpoint", "otlp.iapm.app:443", "OTLP gRPC endpoint (host:port)")
	apiKeyFlag := flag.String("apikey", "", "API key for OTLP endpoint (required)")
	level := flag.Int("level", 1, "aggressiveness 1-10 (1=whisper, 10=SCREAM)")
	errors := flag.Int("errors", 0, "error rate 0-10 (0=none, 5=normal, 10=chaos)")
	noConsumers := flag.Bool("no-consumers", false, "disable all async consumers (messages published but never consumed)")
	insecureFlag := flag.Bool("insecure", false, "use plaintext gRPC (no TLS) for local backends")
	noAIBackendsFlag := flag.Bool("no-ai-backends", false, "disable all LLM/AI backends (AI spans emit errors)")
	aiOnlyFlag := flag.Bool("ai-only", false, "only run AI agentic scenarios")
	complexityFlag := flag.String("complexity", "normal", "topology complexity: light, normal, heavy")
	flag.Parse()
	consumersEnabled = !*noConsumers
	insecureMode = *insecureFlag
	noAIBackends = *noAIBackendsFlag
	aiOnly = *aiOnlyFlag
	complexity = *complexityFlag
	if complexity != "light" && complexity != "normal" && complexity != "heavy" {
		log.Fatalf("invalid complexity %q — must be light, normal, or heavy", complexity)
	}

	endpoint := *endpointFlag
	apiKey := *apiKeyFlag
	if apiKey == "" {
		// Check environment variable as fallback
		apiKey = os.Getenv("OTEL_APIKEY")
	}
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: API key required. Use -apikey flag or set OTEL_APIKEY environment variable.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  tracegen -apikey YOUR_KEY [-complexity light|normal|heavy] [-level N] [-errors N]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Get your API key at https://iapm.app")
		os.Exit(1)
	}

	cfg, ok := levels[*level]
	if !ok {
		log.Fatalf("invalid level %d — must be 1-10", *level)
	}
	if *errors < 0 || *errors > 10 {
		log.Fatalf("invalid errors %d — must be 0-10", *errors)
	}
	errorMultiplier = float64(*errors) / 5.0 // 0=0x, 5=1x, 10=2x

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Service definitions with replica ranges — pods are generated at startup
	// tier: "light" = core backbone, "normal" = full traditional, "heavy" = includes AI
	type serviceSpec struct {
		name    string // service name
		prefix  string // pod ID prefix
		hash    string // deployment hash (6 hex chars)
		minPods int    // minimum replicas
		maxPods int    // maximum replicas
		isAI    bool   // AI service — excluded when -no-ai-backends
		tier    string // minimum complexity tier to include this service
	}
	services := []serviceSpec{
		// Core backbone (light) — 10 services
		{"web-frontend", "web-frontend", "d7b48c", 2, 4, false, "light"},
		{"api-gateway", "api-gateway", "e5a21b", 3, 6, false, "light"},
		{"order-service", "order-svc", "f3c91a", 2, 5, false, "light"},
		{"payment-service", "payment-svc", "a82d4e", 2, 4, false, "light"},
		{"inventory-service", "inventory-svc", "b46e5f", 2, 4, false, "light"},
		{"user-service", "user-svc", "d63a7b", 2, 4, false, "light"},
		{"cache-service", "cache-svc", "e71b4c", 3, 5, false, "light"},
		{"auth-service", "auth-svc", "b47e2f", 3, 5, false, "light"},
		{"product-service", "product-svc", "e85c6f", 3, 5, false, "light"},
		{"cart-service", "cart-svc", "d72b5e", 2, 4, false, "light"},
		// Extended traditional (normal) — adds 10 more
		{"notification-service", "notif-svc", "c59f2a", 2, 4, false, "normal"},
		{"search-service", "search-svc", "f85c3d", 2, 4, false, "normal"},
		{"scheduler-service", "scheduler-svc", "a93d1e", 1, 1, false, "normal"},
		{"recommendation-service", "rec-svc", "c58a3c", 2, 4, false, "normal"},
		{"shipping-service", "shipping-svc", "f96d7a", 2, 4, false, "normal"},
		{"fraud-service", "fraud-svc", "a17e8b", 2, 3, false, "normal"},
		{"email-service", "email-svc", "b28f9c", 2, 4, false, "normal"},
		{"tax-service", "tax-svc", "c39a1d", 1, 2, false, "normal"},
		{"analytics-service", "analytics-svc", "d41b2e", 3, 5, false, "normal"},
		{"config-service", "config-svc", "e52c3f", 1, 2, false, "normal"},
		// AI services (heavy) — adds 8 more
		{"llm-gateway", "llm-gw", "a23b4c", 2, 5, true, "heavy"},
		{"embedding-service", "embed-svc", "b34c5d", 2, 4, true, "heavy"},
		{"vector-db-service", "vecdb-svc", "c45d6e", 2, 3, true, "heavy"},
		{"ai-agent-service", "agent-svc", "d56e7f", 2, 4, true, "heavy"},
		{"content-moderation-service", "modsvc", "e67f8a", 2, 3, true, "heavy"},
		{"model-registry-service", "modelreg", "f78a9b", 1, 1, true, "heavy"},
		{"feature-store-service", "featstore", "a89b1c", 2, 3, true, "heavy"},
		{"data-pipeline-service", "pipeline", "b91c2d", 2, 3, true, "heavy"},
	}

	// tierIncluded checks if a service's tier is active for the current complexity
	tierIncluded := func(tier string) bool {
		switch complexity {
		case "heavy":
			return true
		case "normal":
			return tier == "light" || tier == "normal"
		default: // light
			return tier == "light"
		}
	}

	// AKS VMSS nodes (2 node pools, 5 nodes total)
	nodes := []string{
		"aks-userpool1-38437823-vmss000000",
		"aks-userpool1-38437823-vmss000001",
		"aks-userpool1-38437823-vmss000002",
		"aks-userpool2-52891647-vmss000000",
		"aks-userpool2-52891647-vmss000001",
	}

	// Deterministic pod suffix pool for realistic instance IDs
	suffixes := []string{
		"x2km9", "n3pr7", "j4hk8", "w2tp6", "m9qv1", "h5rd3", "k8nj2", "p7tw4",
		"t7mw4", "q1vx6", "k2pn8", "j9dr4", "m3wh7", "x6kv1", "p4td2", "n8jq5",
		"h6wm3", "k9tp1", "r2vd7", "j5mw2", "t8nk4", "n1ph6", "m4kr8", "w2jt9",
		"p6hn3", "h7vn2", "p5tk8", "j3kn9", "w8pm4", "m2hr7", "n9tv5", "k4jw3",
		"p5tn2", "h8km6", "j6wr4", "m3tp1", "k4hn7", "w9jm5", "n2vp8", "h5km3",
		"p8jt6", "w2nr9", "m7wk4", "k7mn2", "p9qr5", "h4st8", "j2vw6", "m8xy3",
		"n5ab9", "q7cd4", "r3ef1", "t6gh7", "u9ij5", "v2kl8", "w5mn3", "x8op6",
		"y1qr9", "z4st2", "a7uv5", "b3wx8",
	}

	// Generate pods from service specs with replica ranges
	type pod struct{ svc, id, node string }
	var pods []pod
	suffixIdx := 0
	totalServices := 0
	for _, svc := range services {
		if !tierIncluded(svc.tier) {
			continue
		}
		if svc.isAI && noAIBackends {
			continue
		}
		totalServices++
		replicas := svc.minPods
		if complexity == "light" {
			replicas = svc.minPods // always use minimum in light mode
		} else if svc.maxPods > svc.minPods {
			replicas = svc.minPods + rand.Intn(svc.maxPods-svc.minPods+1)
		}
		for r := 0; r < replicas; r++ {
			suffix := suffixes[suffixIdx%len(suffixes)]
			suffixIdx++
			podID := fmt.Sprintf("%s-%s-%s", svc.prefix, svc.hash, suffix)
			node := nodes[(suffixIdx+r)%len(nodes)]
			pods = append(pods, pod{svc.name, podID, node})
		}
	}

	for _, p := range pods {
		newProvider(ctx, p.svc, endpoint, apiKey, p.id, p.node)
	}
	defer func() {
		for _, tp := range providers {
			tp.Shutdown(context.Background())
		}
	}()

	ticker := time.NewTicker(time.Duration(cfg.tickMs) * time.Millisecond)
	defer ticker.Stop()

	type namedScenario struct {
		name     string
		fn       func(context.Context)
		isError  bool   // true = dedicated error scenario, excluded when -errors=0
		category string // "traditional", "ai", or "chaos"
		tier     string // minimum complexity tier: "light", "normal", "heavy"
	}
	allScenarios := []namedScenario{
		// Core scenarios (light) — clean demo flows
		{"Create Order", createOrderFlow, false, "traditional", "light"},
		{"Search & Browse", searchAndBrowseFlow, false, "traditional", "light"},
		{"User Login", userLoginFlow, false, "traditional", "light"},
		{"Add to Cart", addToCartFlow, false, "traditional", "light"},
		{"Full Checkout", fullCheckoutFlow, false, "traditional", "light"},
		{"Health Check", healthCheckFlow, false, "traditional", "light"},
		// Extended scenarios (normal)
		{"Failed Payment", failedPaymentFlow, true, "chaos", "normal"},
		{"Bulk Notifications", bulkNotificationFlow, false, "traditional", "normal"},
		{"Inventory Sync", inventorySyncFlow, false, "traditional", "normal"},
		{"Scheduled Report", scheduledReportFlow, false, "traditional", "normal"},
		{"Stripe Webhook", stripeWebhookFlow, false, "traditional", "normal"},
		{"Recommendations", recommendationFlow, false, "traditional", "normal"},
		{"Shipping Update", shippingUpdateFlow, false, "traditional", "normal"},
		{"Return/Refund", returnRefundFlow, false, "traditional", "normal"},
		{"Saga Compensation", sagaCompensationFlow, true, "chaos", "normal"},
		{"Timeout Cascade", timeoutCascadeFlow, true, "chaos", "normal"},
		// AI agentic scenarios (heavy)
		{"RAG Search", ragSearchFlow, false, "ai", "heavy"},
		{"AI Chatbot", aiChatbotFlow, false, "ai", "heavy"},
		{"Content Moderation", contentModerationFlow, false, "ai", "heavy"},
		{"Multi-Step Agent", multiStepAgentFlow, false, "ai", "heavy"},
	}
	var scenarios []namedScenario
	for _, s := range allScenarios {
		if !tierIncluded(s.tier) {
			continue
		}
		if s.isError && errorMultiplier == 0 {
			continue
		}
		if aiOnly && s.category != "ai" {
			continue
		}
		if noAIBackends && s.category == "ai" {
			continue
		}
		scenarios = append(scenarios, s)
	}

	errorLabels := []string{"none", "rare", "rare", "low", "low", "normal", "elevated", "high", "high", "extreme", "chaos"}
	fmt.Println()
	fmt.Printf("OpenTelemetry Trace Generator (%d services, %d pods, %d scenarios)\n", totalServices, len(pods), len(scenarios))
	fmt.Printf("Endpoint: %s  Complexity: %s\n", endpoint, complexity)
	fmt.Printf("Level %d: %s  (tick=%dms, burst=%d-%d)  Errors: %s (%d)\n",
		*level, cfg.label, cfg.tickMs, cfg.burstMin, cfg.burstMax, errorLabels[*errors], *errors)
	if aiOnly && noAIBackends {
		fmt.Fprintln(os.Stderr, "Error: -ai-only and -no-ai-backends are mutually exclusive.")
		os.Exit(1)
	}
	if aiOnly {
		fmt.Println("Mode: AI-only (traditional scenarios excluded)")
	}
	if noAIBackends {
		fmt.Println("Mode: No AI backends (AI services excluded)")
	}
	if len(scenarios) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no scenarios available with current flags.")
		os.Exit(1)
	}
	fmt.Println("Press Ctrl+C to stop.")
	fmt.Println()

	sent := 0
	for {
		select {
		case <-ctx.Done():
			fmt.Printf("\nShutting down, flushing %d traces...\n", sent)
			return
		case <-ticker.C:
			burst := cfg.burstMin + rand.Intn(cfg.burstMax-cfg.burstMin+1)
			for b := 0; b < burst; b++ {
				s := scenarios[rand.Intn(len(scenarios))]
				sent++
				if sent%50 == 0 {
					fmt.Printf("[%s] %d traces sent  (%d services, %d pods, %d scenarios)\n", time.Now().Format("15:04:05"), sent, totalServices, len(pods), len(scenarios))
				}
				go s.fn(ctx)
			}
		}
	}
}

// Full order flow: UI -> gateway -> order -> payment -> inventory -> notification
func createOrderFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "PlaceOrder",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/orders"),
			attribute.String("browser.page", "/checkout"),
			attribute.String("user.session_id", randomUserID()),
		),
	)
	defer ui.End()
	sleep(2, 8)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/orders",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/orders"),
			attribute.Int("http.status_code", 200),
			attribute.String("http.user_agent", "Mozilla/5.0"),
			attribute.String("net.peer.ip", randomIP()),
		),
	)
	defer gateway.End()
	sleep(10, 30)

	// Auth check
	_, auth := tracer("user-service").Start(ctx, "ValidateToken",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateToken"),
			attribute.String("user.id", randomUserID()),
		),
	)
	sleep(5, 15)
	auth.End()

	// Cache lookup
	_, cache := tracer("cache-service").Start(ctx, "Redis GET user-session",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "GET"),
			attribute.String("db.redis.key", "session:"+randomUserID()),
			attribute.Bool("cache.hit", !errorChance(0.3)),
		),
	)
	sleep(1, 3)
	if errorChance(0.03) {
		exc := randomException(cacheExceptions)
		recordException(cache, exc.excType, exc.message, exc.stacktrace)
	}
	cache.End()

	// Create order
	ctx2, order := tracer("order-service").Start(ctx, "CreateOrder",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("order.id", randomOrderID()),
			attribute.Float64("order.total", 50+rand.Float64()*500),
			attribute.Int("order.items", 1+rand.Intn(8)),
		),
	)
	sleep(20, 60)

	// DB write
	_, dbWrite := tracer("order-service").Start(ctx2, "INSERT orders",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "INSERT"),
			attribute.String("db.name", "orders_db"),
			attribute.String("db.statement", "INSERT INTO orders (id, user_id, total) VALUES ($1, $2, $3)"),
		),
	)
	sleep(5, 25)
	if errorChance(0.05) {
		exc := randomException(dbExceptions)
		recordException(dbWrite, exc.excType, exc.message, exc.stacktrace)
	}
	dbWrite.End()

	// Publish to queue
	_, publish := tracer("order-service").Start(ctx2, "rabbitmq publish orders.created",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "publish"),
			attribute.String("messaging.destination", "orders.created"),
		),
	)
	sleep(2, 8)
	publish.End()
	order.End()

	// Queue delivery delay — message sits in broker before consumer picks up
	sleep(50, 300)

	if !consumersEnabled {
		return // messages pile up, no consumers running
	}

	// 5% chance: message lost — payment consumer never fires
	if errorChance(0.05) {
		return
	}

	// Payment processing (async worker picks up)
	ctx3, payment := tracer("payment-service").Start(ctx, "ProcessPayment",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "receive"),
			attribute.String("messaging.destination", "payments.process"),
			attribute.String("payment.provider", "stripe"),
		),
	)
	sleep(100, 400)

	// Diamond dependency: payment also checks cache (same as order-service above)
	_, payCache := tracer("cache-service").Start(ctx3, "Redis GET payment-idempotency",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "GET"),
			attribute.String("db.redis.key", "idempotent:"+randomOrderID()),
		),
	)
	sleep(1, 3)
	payCache.End()

	// External Stripe call
	_, stripe := tracer("payment-service").Start(ctx3, "POST https://api.stripe.com/v1/charges",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "https://api.stripe.com/v1/charges"),
			attribute.String("peer.service", "stripe-api"),
			attribute.Int("http.status_code", 200),
		),
	)
	sleep(80, 300)
	stripe.End()
	payment.End()

	// Queue delivery delay
	sleep(30, 150)

	// 5% chance: message lost — inventory consumer never fires
	if errorChance(0.05) {
		return
	}

	// Inventory update
	ctx4, inventory := tracer("inventory-service").Start(ctx, "ReserveStock",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "receive"),
			attribute.String("messaging.destination", "inventory.reserve"),
		),
	)
	sleep(30, 80)

	_, dbUpdate := tracer("inventory-service").Start(ctx4, "UPDATE inventory SET quantity = quantity - $1",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "UPDATE"),
			attribute.String("db.name", "inventory_db"),
		),
	)
	sleep(10, 40)
	dbUpdate.End()
	inventory.End()

	// Queue delivery delay
	sleep(20, 100)

	// 5% chance: message lost — notification consumer never fires
	if errorChance(0.05) {
		return
	}

	// Notification
	_, notify := tracer("notification-service").Start(ctx, "SendOrderConfirmation",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.operation", "receive"),
			attribute.String("messaging.destination", "notifications.email"),
			attribute.String("peer.service", "sendgrid"),
			attribute.String("notification.type", "order_confirmation"),
		),
	)
	sleep(50, 200)
	notify.End()
}

// Search flow: UI -> gateway -> cache -> search
func searchAndBrowseFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "SearchProducts",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.url", "/api/v2/search"),
			attribute.String("browser.page", "/search"),
			attribute.String("search.query", randomSearchTerm()),
		),
	)
	defer ui.End()
	sleep(2, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "GET /api/v2/search",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.route", "/api/v2/search"),
			attribute.Int("http.status_code", 200),
			attribute.String("search.query", randomSearchTerm()),
		),
	)
	defer gateway.End()
	sleep(5, 15)

	// Cache check
	_, cache := tracer("cache-service").Start(ctx, "Redis GET search-cache",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "GET"),
			attribute.Bool("cache.hit", false),
		),
	)
	sleep(1, 3)
	cache.End()

	// Elasticsearch query
	ctx2, search := tracer("search-service").Start(ctx, "Elasticsearch Query",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("db.system", "elasticsearch"),
			attribute.String("db.operation", "search"),
			attribute.Int("search.results_count", rand.Intn(50)),
		),
	)
	sleep(30, 120)

	_, esQuery := tracer("search-service").Start(ctx2, "POST /products/_search",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("db.statement", `{"query":{"match":{"name":"widget"}}}`),
		),
	)
	sleep(20, 80)
	if errorChance(0.04) {
		exc := randomException(searchExceptions)
		recordException(esQuery, exc.excType, exc.message, exc.stacktrace)
	}
	esQuery.End()
	search.End()

	// Cache write
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
}

// Login flow with occasional auth failures
func userLoginFlow(ctx context.Context) {
	isFailure := errorChance(0.15)

	ctx, ui := tracer("web-frontend").Start(ctx, "UserLogin",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/auth/login"),
			attribute.String("browser.page", "/login"),
		),
	)
	defer ui.End()
	sleep(1, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/auth/login",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/auth/login"),
		),
	)
	defer func() {
		if isFailure {
			gateway.SetAttributes(attribute.Int("http.status_code", 401))
			gateway.SetStatus(codes.Error, "authentication failed")
		} else {
			gateway.SetAttributes(attribute.Int("http.status_code", 200))
		}
		gateway.End()
	}()
	sleep(5, 10)

	ctx2, auth := tracer("user-service").Start(ctx, "AuthenticateUser",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "Authenticate"),
		),
	)
	sleep(10, 30)

	// DB lookup
	_, dbLookup := tracer("user-service").Start(ctx2, "SELECT users WHERE email = $1",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.name", "users_db"),
		),
	)
	sleep(5, 20)
	dbLookup.End()

	if isFailure {
		exc := randomException(authExceptions)
		recordException(auth, exc.excType, exc.message, exc.stacktrace)
		auth.End()
		return
	}
	auth.End()

	// On success, create session
	_, session := tracer("cache-service").Start(ctx, "Redis SET user-session",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "SET"),
			attribute.String("db.redis.key", "session:"+randomUserID()),
			attribute.Int("db.redis.ttl_seconds", 3600),
		),
	)
	sleep(1, 3)
	session.End()
}

// Payment failure flow — Stripe returns 402
func failedPaymentFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "PlaceOrder",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/orders"),
			attribute.String("browser.page", "/checkout"),
		),
	)
	defer ui.End()
	sleep(2, 8)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/orders",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/orders"),
			attribute.Int("http.status_code", 402),
			attribute.String("net.peer.ip", randomIP()),
		),
	)
	exc := randomException(paymentExceptions)
	recordException(gateway, exc.excType, exc.message, exc.stacktrace)
	defer gateway.End()
	sleep(10, 20)

	// Order created
	ctx2, order := tracer("order-service").Start(ctx, "CreateOrder",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("order.id", randomOrderID()),
			attribute.String("order.status", "payment_failed"),
		),
	)
	sleep(15, 40)

	_, dbWrite := tracer("order-service").Start(ctx2, "INSERT orders",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "INSERT"),
		),
	)
	sleep(5, 15)
	dbWrite.End()
	order.End()

	// Queue delivery delay
	sleep(50, 300)

	if !consumersEnabled {
		return // messages pile up, no consumers running
	}

	// Payment fails
	ctx3, payment := tracer("payment-service").Start(ctx, "ProcessPayment",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("payment.provider", "stripe"),
			attribute.String("payment.status", "declined"),
		),
	)
	recordException(payment, exc.excType, exc.message, exc.stacktrace)
	sleep(80, 250)

	_, stripe := tracer("payment-service").Start(ctx3, "POST https://api.stripe.com/v1/charges",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("peer.service", "stripe-api"),
			attribute.Int("http.status_code", 402),
			attribute.String("error.type", "card_declined"),
		),
	)
	recordException(stripe, exc.excType, exc.message, exc.stacktrace)
	sleep(60, 200)
	stripe.End()
	payment.End()

	// Queue delivery delay
	sleep(20, 100)

	// Notify user of failure
	_, notify := tracer("notification-service").Start(ctx, "SendPaymentFailedEmail",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("peer.service", "sendgrid"),
			attribute.String("notification.type", "payment_failed"),
		),
	)
	sleep(40, 150)
	notify.End()
}

// Bulk notification flow — admin triggers digest from UI
func bulkNotificationFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "TriggerDigest",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/admin/digest"),
			attribute.String("browser.page", "/admin/notifications"),
		),
	)
	defer ui.End()
	sleep(1, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/admin/digest",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/admin/digest"),
			attribute.Int("http.status_code", 202),
		),
	)
	defer gateway.End()
	sleep(3, 10)

	ctx, scheduler := tracer("order-service").Start(ctx, "DailyDigestJob",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("scheduling.job", "daily-digest"),
			attribute.Int("batch.size", 5+rand.Intn(20)),
		),
	)
	defer scheduler.End()
	sleep(10, 30)

	// DB query for pending notifications
	_, dbRead := tracer("order-service").Start(ctx, "SELECT pending_notifications",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.statement", "SELECT * FROM notifications WHERE status = 'pending' LIMIT 100"),
		),
	)
	sleep(15, 50)
	dbRead.End()

	// Publish to notification queue
	_, publish := tracer("order-service").Start(ctx, "rabbitmq publish notifications.digest",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "publish"),
			attribute.String("messaging.destination", "notifications.digest"),
			attribute.Int("messaging.batch_size", 3+rand.Intn(3)),
		),
	)
	sleep(2, 8)
	publish.End()

	if !consumersEnabled {
		return // messages pile up, no consumers running
	}

	// Queue delivery delay
	sleep(30, 100)

	// Fan out 3-5 notifications
	count := 3 + rand.Intn(3)
	for i := 0; i < count; i++ {
		_, notify := tracer("notification-service").Start(ctx, fmt.Sprintf("SendDigestEmail #%d", i+1),
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("peer.service", "sendgrid"),
				attribute.String("notification.type", "daily_digest"),
				attribute.Int("notification.batch_index", i),
			),
		)
		sleep(30, 100)

		if errorChance(0.1) {
			exc := randomException(httpExceptions)
			recordException(notify, exc.excType, exc.message, exc.stacktrace)
		}
		notify.End()
	}
}

// Health check flow — lightweight, high-frequency pings across services
func healthCheckFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "HealthDashboard",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.url", "/api/v2/healthz"),
			attribute.String("browser.page", "/admin/health"),
		),
	)
	defer ui.End()
	sleep(1, 3)

	ctx, gateway := tracer("api-gateway").Start(ctx, "GET /healthz",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.route", "/healthz"),
			attribute.Int("http.status_code", 200),
		),
	)
	defer gateway.End()
	sleep(1, 5)

	// Fan-out health checks to all backend services concurrently
	type check struct {
		svc  string
		span string
	}
	checks := []check{
		{"order-service", "GET /health"},
		{"payment-service", "GET /health"},
		{"inventory-service", "GET /health"},
		{"user-service", "GET /health"},
		{"cache-service", "Redis PING"},
		{"search-service", "GET /_cluster/health"},
	}
	done := make(chan struct{}, len(checks))
	for _, c := range checks {
		go func(c check) {
			_, span := tracer(c.svc).Start(ctx, c.span,
				trace.WithSpanKind(trace.SpanKindClient),
				trace.WithAttributes(
					attribute.String("health.service", c.svc),
					attribute.Bool("health.ok", !errorChance(0.05)),
				),
			)
			sleep(1, 10)
			if errorChance(0.05) {
				exc := randomException(httpExceptions)
				recordException(span, exc.excType, exc.message, exc.stacktrace)
			}
			span.End()
			done <- struct{}{}
		}(c)
	}
	for i := 0; i < len(checks); i++ {
		<-done
	}
}

// Inventory sync flow — admin triggers from UI
func inventorySyncFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "TriggerInventorySync",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/admin/inventory/sync"),
			attribute.String("browser.page", "/admin/inventory"),
		),
	)
	defer ui.End()
	sleep(1, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/admin/inventory/sync",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/admin/inventory/sync"),
			attribute.Int("http.status_code", 202),
		),
	)
	defer gateway.End()
	sleep(3, 10)

	ctx, job := tracer("inventory-service").Start(ctx, "InventorySyncJob",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("scheduling.job", "inventory-sync"),
			attribute.Int("batch.size", 20+rand.Intn(80)),
		),
	)
	defer job.End()
	sleep(5, 15)

	// Read from DB
	_, dbRead := tracer("inventory-service").Start(ctx, "SELECT inventory WHERE updated_at > $1",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.name", "inventory_db"),
			attribute.Int("db.rows_affected", 20+rand.Intn(80)),
		),
	)
	sleep(15, 60)
	dbRead.End()

	// Warm cache with multiple concurrent SET operations
	cacheOps := 3 + rand.Intn(5)
	done := make(chan struct{}, cacheOps)
	for i := 0; i < cacheOps; i++ {
		go func(i int) {
			_, cacheSet := tracer("cache-service").Start(ctx, fmt.Sprintf("Redis MSET inventory-batch-%d", i),
				trace.WithSpanKind(trace.SpanKindClient),
				trace.WithAttributes(
					attribute.String("db.system", "redis"),
					attribute.String("db.operation", "MSET"),
					attribute.Int("db.redis.keys_count", 5+rand.Intn(15)),
				),
			)
			sleep(2, 8)
			cacheSet.End()
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < cacheOps; i++ {
		<-done
	}

	// Notify search service to reindex
	_, reindex := tracer("search-service").Start(ctx, "POST /products/_bulk",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "elasticsearch"),
			attribute.String("db.operation", "bulk_index"),
			attribute.Int("db.docs_count", 10+rand.Intn(40)),
		),
	)
	sleep(30, 120)
	reindex.End()
}

// Scheduled report — scheduler-service is the root, no UI or gateway entry point.
// Creates a long chain: scheduler -> order -> inventory -> cache -> notification
func scheduledReportFlow(ctx context.Context) {
	ctx, scheduler := tracer("scheduler-service").Start(ctx, "CronJob: DailyReport",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("scheduling.trigger", "cron"),
			attribute.String("scheduling.schedule", "0 6 * * *"),
			attribute.String("scheduling.job_id", fmt.Sprintf("job_%06d", rand.Intn(999999))),
		),
	)
	defer scheduler.End()
	sleep(5, 15)

	// Auth check — scheduler authenticates via service account
	_, auth := tracer("auth-service").Start(ctx, "ValidateServiceAccount",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateServiceAccount"),
			attribute.String("auth.principal", "scheduler-sa"),
		),
	)
	sleep(3, 10)
	auth.End()

	// Aggregate order stats
	ctx2, orderAgg := tracer("order-service").Start(ctx, "AggregateOrderStats",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("report.type", "daily_summary"),
			attribute.Int("report.period_hours", 24),
		),
	)
	sleep(20, 60)

	_, dbQuery := tracer("order-service").Start(ctx2, "SELECT orders WHERE created_at > $1 GROUP BY status",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.name", "orders_db"),
			attribute.Int("db.rows_affected", 50+rand.Intn(500)),
		),
	)
	sleep(30, 120)
	if errorChance(0.04) {
		exc := randomException(dbExceptions)
		recordException(dbQuery, exc.excType, exc.message, exc.stacktrace)
	}
	dbQuery.End()
	orderAgg.End()

	// Inventory summary
	ctx3, invSummary := tracer("inventory-service").Start(ctx, "InventorySummary",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("report.section", "inventory_levels"),
		),
	)
	sleep(15, 40)

	_, invDB := tracer("inventory-service").Start(ctx3, "SELECT inventory GROUP BY category",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.name", "inventory_db"),
		),
	)
	sleep(20, 80)
	invDB.End()
	invSummary.End()

	// Cache the report
	_, cacheSet := tracer("cache-service").Start(ctx, "Redis SET daily-report",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "SET"),
			attribute.String("db.redis.key", "report:daily:"+time.Now().Format("2006-01-02")),
			attribute.Int("db.redis.ttl_seconds", 86400),
		),
	)
	sleep(2, 5)
	cacheSet.End()

	// Publish report-ready event
	_, publish := tracer("scheduler-service").Start(ctx, "rabbitmq publish reports.ready",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "publish"),
			attribute.String("messaging.destination", "reports.ready"),
		),
	)
	sleep(2, 5)
	publish.End()

	if !consumersEnabled {
		return
	}

	sleep(20, 80)

	// Notification consumer sends report email
	_, notify := tracer("notification-service").Start(ctx, "SendDailyReportEmail",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "receive"),
			attribute.String("peer.service", "sendgrid"),
			attribute.String("notification.type", "daily_report"),
		),
	)
	sleep(40, 150)
	if errorChance(0.08) {
		exc := randomException(httpExceptions)
		recordException(notify, exc.excType, exc.message, exc.stacktrace)
	}
	notify.End()
}

// Stripe webhook — external callback entering at payment-service directly (no gateway).
// payment-service -> auth-service (verify sig) -> order-service -> cache -> notification
func stripeWebhookFlow(ctx context.Context) {
	ctx, webhook := tracer("payment-service").Start(ctx, "POST /webhooks/stripe",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/webhooks/stripe"),
			attribute.String("webhook.event", "charge.succeeded"),
			attribute.String("webhook.id", fmt.Sprintf("evt_%016d", rand.Int63())),
			attribute.String("peer.service", "stripe-api"),
		),
	)
	defer webhook.End()
	sleep(5, 15)

	// Verify webhook signature via auth-service
	_, verify := tracer("auth-service").Start(ctx, "VerifyWebhookSignature",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "VerifyWebhookSignature"),
			attribute.String("auth.provider", "stripe"),
		),
	)
	sleep(2, 8)
	if errorChance(0.03) {
		exc := exceptionInfo{
			"System.Security.SecurityException",
			"Webhook signature verification failed: timestamp too old",
			`System.Security.SecurityException: Webhook signature verification failed: timestamp too old
   at AuthService.Webhooks.StripeSignatureVerifier.Verify(String payload, String signature) in /src/AuthService/Webhooks/StripeSignatureVerifier.cs:line 42`,
		}
		recordException(verify, exc.excType, exc.message, exc.stacktrace)
		verify.End()
		return
	}
	verify.End()

	// Update order status
	ctx2, orderUpdate := tracer("order-service").Start(ctx, "UpdateOrderPaymentStatus",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("order.id", randomOrderID()),
			attribute.String("order.status", "payment_confirmed"),
		),
	)
	sleep(10, 30)

	_, dbUpdate := tracer("order-service").Start(ctx2, "UPDATE orders SET status = $1 WHERE stripe_id = $2",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "UPDATE"),
			attribute.String("db.name", "orders_db"),
		),
	)
	sleep(5, 20)
	dbUpdate.End()
	orderUpdate.End()

	// Cache invalidation — diamond dependency (both order-service and payment-service hit cache)
	_, cacheInval := tracer("cache-service").Start(ctx, "Redis DEL order-cache",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "DEL"),
			attribute.String("db.redis.key", "order:status:*"),
		),
	)
	sleep(1, 3)
	cacheInval.End()

	// Publish order-confirmed event
	_, publish := tracer("payment-service").Start(ctx, "rabbitmq publish orders.confirmed",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "publish"),
			attribute.String("messaging.destination", "orders.confirmed"),
		),
	)
	sleep(2, 5)
	publish.End()

	if !consumersEnabled {
		return
	}

	sleep(20, 80)

	// Notification: send receipt
	_, notify := tracer("notification-service").Start(ctx, "SendPaymentReceiptEmail",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "receive"),
			attribute.String("peer.service", "sendgrid"),
			attribute.String("notification.type", "payment_receipt"),
		),
	)
	sleep(30, 120)
	notify.End()
}

// Recommendation flow — scatter-gather / bowtie pattern.
// web-frontend -> api-gateway -> recommendation-service -> fan-out to search+inventory+user -> fan-in -> cache
func recommendationFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "GetRecommendations",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.url", "/api/v2/recommendations"),
			attribute.String("browser.page", "/products"),
			attribute.String("user.session_id", randomUserID()),
		),
	)
	defer ui.End()
	sleep(2, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "GET /api/v2/recommendations",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.route", "/api/v2/recommendations"),
			attribute.Int("http.status_code", 200),
		),
	)
	defer gateway.End()
	sleep(3, 10)

	// Auth check
	_, auth := tracer("auth-service").Start(ctx, "ValidateToken",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateToken"),
		),
	)
	sleep(2, 8)
	auth.End()

	// Recommendation engine — the bowtie center
	ctx2, recEngine := tracer("recommendation-service").Start(ctx, "ComputeRecommendations",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("ml.model", "collaborative-filter-v3"),
			attribute.Int("ml.candidates", 50+rand.Intn(200)),
			attribute.String("user.id", randomUserID()),
		),
	)
	defer recEngine.End()
	sleep(10, 30)

	// Scatter: fan-out to 3 services concurrently
	done := make(chan struct{}, 3)

	// 1. Search for similar products
	go func() {
		_, searchSpan := tracer("search-service").Start(ctx2, "SimilarProducts query",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "elasticsearch"),
				attribute.String("db.operation", "search"),
				attribute.String("search.type", "more_like_this"),
				attribute.Int("search.results_count", 10+rand.Intn(30)),
			),
		)
		sleep(20, 80)
		if errorChance(0.05) {
			exc := randomException(searchExceptions)
			recordException(searchSpan, exc.excType, exc.message, exc.stacktrace)
		}
		searchSpan.End()
		done <- struct{}{}
	}()

	// 2. Check inventory for available items
	go func() {
		_, invSpan := tracer("inventory-service").Start(ctx2, "CheckAvailability batch",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "postgresql"),
				attribute.String("db.operation", "SELECT"),
				attribute.Int("batch.size", 20+rand.Intn(30)),
			),
		)
		sleep(15, 50)
		invSpan.End()
		done <- struct{}{}
	}()

	// 3. User purchase history & preferences
	go func() {
		_, userSpan := tracer("user-service").Start(ctx2, "GetUserPreferences",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("rpc.system", "grpc"),
				attribute.String("rpc.service", "UserService"),
				attribute.String("rpc.method", "GetPreferences"),
			),
		)
		sleep(10, 40)
		userSpan.End()
		done <- struct{}{}
	}()

	// Gather: wait for all 3
	for i := 0; i < 3; i++ {
		<-done
	}

	// ML scoring after gather
	sleep(15, 50)

	// Cache the recommendations
	_, cacheSet := tracer("cache-service").Start(ctx2, "Redis SET user-recommendations",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "SET"),
			attribute.String("db.redis.key", "recs:"+randomUserID()),
			attribute.Int("db.redis.ttl_seconds", 600),
		),
	)
	sleep(1, 3)
	cacheSet.End()
}

// Add to cart flow: web -> gateway -> config -> auth -> product -> cart -> cache -> analytics
func addToCartFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "AddToCart",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/cart/items"),
			attribute.String("browser.page", "/products/detail"),
			attribute.String("user.session_id", randomUserID()),
		),
	)
	defer ui.End()
	sleep(1, 5)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/cart/items",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/cart/items"),
			attribute.Int("http.status_code", 200),
		),
	)
	defer gateway.End()
	sleep(3, 10)

	// Feature flag check — show bundled recommendations?
	_, flag := tracer("config-service").Start(ctx, "GetFeatureFlag",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("feature_flag.key", "cart.show_bundles"),
			attribute.Bool("feature_flag.enabled", rand.Intn(2) == 0),
			attribute.String("rpc.system", "grpc"),
		),
	)
	sleep(1, 3)
	flag.End()

	// Auth
	_, auth := tracer("auth-service").Start(ctx, "ValidateToken",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateToken"),
		),
	)
	sleep(2, 8)
	auth.End()

	// Get product details + verify price
	ctx2, product := tracer("product-service").Start(ctx, "GetProduct",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("product.id", fmt.Sprintf("prod_%06d", rand.Intn(999999))),
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "ProductService"),
		),
	)
	sleep(5, 15)

	_, productDB := tracer("product-service").Start(ctx2, "SELECT products WHERE id = $1",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.name", "products_db"),
		),
	)
	sleep(3, 12)
	if errorChance(0.03) {
		exc := randomException(dbExceptions)
		recordException(productDB, exc.excType, exc.message, exc.stacktrace)
	}
	productDB.End()

	// Product cache
	_, productCache := tracer("cache-service").Start(ctx2, "Redis GET product:detail",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "GET"),
			attribute.Bool("cache.hit", rand.Intn(3) != 0),
		),
	)
	sleep(1, 3)
	productCache.End()
	product.End()

	// Add item to cart
	ctx3, cart := tracer("cart-service").Start(ctx, "AddItem",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("cart.user_id", randomUserID()),
			attribute.Int("cart.quantity", 1+rand.Intn(3)),
		),
	)
	sleep(5, 15)

	_, cartStore := tracer("cart-service").Start(ctx3, "Redis HSET cart:user:items",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "HSET"),
			attribute.String("db.redis.key", "cart:"+randomUserID()),
		),
	)
	sleep(1, 3)
	cartStore.End()
	cart.End()

	// Async analytics event
	_, analytics := tracer("analytics-service").Start(ctx, "TrackEvent cart.item_added",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", "analytics.events"),
			attribute.String("analytics.event_type", "cart.item_added"),
		),
	)
	sleep(2, 5)
	analytics.End()
}

// Full checkout flow — THE monster chain touching nearly every service.
// web -> gw -> auth -> cart -> product -> tax+shipping(parallel) -> order -> fraud -> payment -> stripe
// -> inventory -> shipping(label) -> cache -> analytics -> [queue] -> notification -> email
func fullCheckoutFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "Checkout",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/checkout"),
			attribute.String("browser.page", "/checkout/confirm"),
			attribute.String("user.session_id", randomUserID()),
		),
	)
	defer ui.End()
	sleep(2, 8)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/checkout",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/checkout"),
			attribute.Int("http.status_code", 200),
			attribute.String("net.peer.ip", randomIP()),
		),
	)
	defer gateway.End()
	sleep(5, 15)

	// Auth
	_, auth := tracer("auth-service").Start(ctx, "ValidateToken",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateToken"),
		),
	)
	sleep(2, 8)
	auth.End()

	// Get cart contents
	ctx2, cart := tracer("cart-service").Start(ctx, "GetCart",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "CartService"),
			attribute.String("rpc.method", "GetCart"),
		),
	)
	sleep(5, 12)

	_, cartRead := tracer("cart-service").Start(ctx2, "Redis HGETALL cart:user:items",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "HGETALL"),
		),
	)
	sleep(1, 3)
	cartRead.End()
	cart.End()

	// Resolve product details + prices
	items := 1 + rand.Intn(5)
	_, product := tracer("product-service").Start(ctx, "GetProducts batch",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "ProductService"),
			attribute.String("rpc.method", "GetProductsBatch"),
			attribute.Int("batch.size", items),
		),
	)
	sleep(8, 25)
	product.End()

	// Tax + shipping rates in parallel
	done := make(chan struct{}, 2)
	go func() {
		_, tax := tracer("tax-service").Start(ctx, "CalculateTax",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("rpc.system", "grpc"),
				attribute.String("rpc.service", "TaxService"),
				attribute.Float64("tax.subtotal", 50+rand.Float64()*500),
				attribute.String("tax.jurisdiction", "US-WA"),
				attribute.Float64("tax.rate", 0.065+rand.Float64()*0.04),
			),
		)
		sleep(8, 25)
		tax.End()
		done <- struct{}{}
	}()

	go func() {
		_, ship := tracer("shipping-service").Start(ctx, "GetShippingRates",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("rpc.system", "grpc"),
				attribute.String("rpc.service", "ShippingService"),
				attribute.String("rpc.method", "GetRates"),
				attribute.Int("shipping.items", items),
				attribute.String("shipping.destination", "US"),
			),
		)
		sleep(15, 50)
		if errorChance(0.03) {
			exc := randomException(httpExceptions)
			recordException(ship, exc.excType, exc.message, exc.stacktrace)
		}
		ship.End()
		done <- struct{}{}
	}()
	for i := 0; i < 2; i++ {
		<-done
	}

	// Create order
	orderID := randomOrderID()
	ctx3, order := tracer("order-service").Start(ctx, "CreateOrder",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("order.id", orderID),
			attribute.Float64("order.total", 50+rand.Float64()*900),
			attribute.Int("order.items", items),
		),
	)
	sleep(15, 40)

	_, dbInsert := tracer("order-service").Start(ctx3, "INSERT orders",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "INSERT"),
			attribute.String("db.name", "orders_db"),
		),
	)
	sleep(5, 20)
	if errorChance(0.03) {
		exc := randomException(dbExceptions)
		recordException(dbInsert, exc.excType, exc.message, exc.stacktrace)
	}
	dbInsert.End()
	order.End()

	// Fraud check with ML scoring
	ctx4, fraud := tracer("fraud-service").Start(ctx, "AnalyzeTransaction",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "FraudService"),
			attribute.String("order.id", orderID),
			attribute.Float64("fraud.score", rand.Float64()*0.3),
			attribute.String("fraud.decision", "approve"),
		),
	)
	sleep(15, 40)

	_, ml := tracer("fraud-service").Start(ctx4, "ML ScoreTransaction",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("ml.model", "fraud-detector-v2"),
			attribute.Int("ml.features", 42),
			attribute.Float64("ml.inference_ms", float64(10+rand.Intn(30))),
		),
	)
	sleep(10, 30)
	ml.End()
	fraud.End()

	// Payment
	ctx5, payment := tracer("payment-service").Start(ctx, "ProcessPayment",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("payment.provider", "stripe"),
			attribute.String("order.id", orderID),
		),
	)
	sleep(20, 50)

	_, idemCache := tracer("cache-service").Start(ctx5, "Redis GET payment-idempotency",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "GET"),
			attribute.String("db.redis.key", "idempotent:"+orderID),
		),
	)
	sleep(1, 3)
	idemCache.End()

	_, stripe := tracer("payment-service").Start(ctx5, "POST https://api.stripe.com/v1/charges",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "https://api.stripe.com/v1/charges"),
			attribute.String("peer.service", "stripe-api"),
			attribute.Int("http.status_code", 200),
		),
	)
	sleep(80, 300)
	stripe.End()
	payment.End()

	// Reserve inventory
	ctx6, inv := tracer("inventory-service").Start(ctx, "ReserveStock",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("order.id", orderID),
			attribute.Int("inventory.items", items),
		),
	)
	sleep(15, 40)

	_, invDB := tracer("inventory-service").Start(ctx6, "UPDATE inventory SET quantity = quantity - $1",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "UPDATE"),
			attribute.String("db.name", "inventory_db"),
		),
	)
	sleep(8, 25)
	invDB.End()
	inv.End()

	// Create shipping label
	_, label := tracer("shipping-service").Start(ctx, "CreateShippingLabel",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("shipping.carrier", "ups"),
			attribute.String("shipping.tracking_number", fmt.Sprintf("1Z%09d", rand.Intn(999999999))),
			attribute.String("order.id", orderID),
		),
	)
	sleep(30, 80)
	label.End()

	// Cache order status
	_, cacheSet := tracer("cache-service").Start(ctx, "Redis SET order:status",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "SET"),
			attribute.String("db.redis.key", "order:"+orderID),
		),
	)
	sleep(1, 3)
	cacheSet.End()

	// Analytics
	_, analyticsEvt := tracer("analytics-service").Start(ctx, "TrackEvent checkout.complete",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", "analytics.events"),
			attribute.String("analytics.event_type", "checkout.complete"),
			attribute.String("order.id", orderID),
		),
	)
	sleep(2, 5)
	analyticsEvt.End()

	// Queue notification
	_, publish := tracer("order-service").Start(ctx, "rabbitmq publish orders.created",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "publish"),
			attribute.String("messaging.destination", "orders.created"),
		),
	)
	sleep(2, 5)
	publish.End()

	sleep(30, 100)
	if !consumersEnabled {
		return
	}

	// Notification -> Email
	ctx7, notify := tracer("notification-service").Start(ctx, "SendOrderConfirmation",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "receive"),
			attribute.String("notification.type", "order_confirmation"),
			attribute.String("order.id", orderID),
		),
	)
	sleep(10, 30)

	_, email := tracer("email-service").Start(ctx7, "SendEmail",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "https://api.sendgrid.com/v3/mail/send"),
			attribute.String("peer.service", "sendgrid"),
			attribute.String("email.template", "order_confirmation"),
		),
	)
	sleep(30, 100)
	if errorChance(0.05) {
		exc := randomException(httpExceptions)
		recordException(email, exc.excType, exc.message, exc.stacktrace)
	}
	email.End()
	notify.End()
}

// Shipping update — carrier webhook enters at shipping-service directly (no UI/gateway).
// shipping -> auth -> order -> cache -> [queue] -> notification -> email -> analytics
func shippingUpdateFlow(ctx context.Context) {
	shippingStatus := []string{"in_transit", "out_for_delivery", "delivered"}[rand.Intn(3)]
	ctx, webhook := tracer("shipping-service").Start(ctx, "POST /webhooks/carrier",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/webhooks/carrier"),
			attribute.String("webhook.carrier", "ups"),
			attribute.String("webhook.event", "tracking.update"),
			attribute.String("shipping.tracking_number", fmt.Sprintf("1Z%09d", rand.Intn(999999999))),
			attribute.String("shipping.status", shippingStatus),
		),
	)
	defer webhook.End()
	sleep(5, 15)

	// Verify webhook signature
	_, verify := tracer("auth-service").Start(ctx, "VerifyWebhookSignature",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("auth.provider", "ups"),
		),
	)
	sleep(2, 8)
	verify.End()

	// Update order status
	ctx2, orderUpdate := tracer("order-service").Start(ctx, "UpdateShippingStatus",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("order.id", randomOrderID()),
			attribute.String("shipping.status", shippingStatus),
		),
	)
	sleep(10, 25)

	_, dbUpdate := tracer("order-service").Start(ctx2, "UPDATE orders SET shipping_status = $1",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "UPDATE"),
			attribute.String("db.name", "orders_db"),
		),
	)
	sleep(5, 15)
	dbUpdate.End()
	orderUpdate.End()

	// Cache invalidation
	_, cacheDel := tracer("cache-service").Start(ctx, "Redis DEL order:status",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "DEL"),
		),
	)
	sleep(1, 3)
	cacheDel.End()

	// Publish notification event
	_, publish := tracer("shipping-service").Start(ctx, "rabbitmq publish shipping.updated",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "publish"),
			attribute.String("messaging.destination", "shipping.updated"),
		),
	)
	sleep(2, 5)
	publish.End()

	if !consumersEnabled {
		return
	}

	sleep(20, 80)

	// Notification -> Email
	ctx3, notify := tracer("notification-service").Start(ctx, "SendShippingUpdate",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "receive"),
			attribute.String("notification.type", "shipping_update"),
		),
	)
	sleep(10, 30)

	_, email := tracer("email-service").Start(ctx3, "SendEmail",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("peer.service", "sendgrid"),
			attribute.String("email.template", "shipping_update"),
		),
	)
	sleep(25, 80)
	email.End()
	notify.End()

	// Analytics
	_, analytics := tracer("analytics-service").Start(ctx, "TrackEvent shipping.status_changed",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", "analytics.events"),
			attribute.String("analytics.event_type", "shipping.status_changed"),
		),
	)
	sleep(2, 5)
	analytics.End()
}

// Saga compensation — payment fails after order+inventory committed, triggering reverse compensations.
// Forward: web -> gw -> auth -> order -> inventory -> fraud -> payment (3 retries, all fail)
// Reverse: saga.compensate -> parallel [cancel order, release inventory, void shipping, notify+email]
func sagaCompensationFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "Checkout",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "/api/v2/checkout"),
			attribute.String("browser.page", "/checkout/confirm"),
		),
	)
	defer ui.End()
	sleep(2, 8)

	ctx, gateway := tracer("api-gateway").Start(ctx, "POST /api/v2/checkout",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "POST"),
			attribute.String("http.route", "/api/v2/checkout"),
			attribute.Int("http.status_code", 402),
		),
	)
	defer gateway.End()
	sleep(5, 15)

	_, auth := tracer("auth-service").Start(ctx, "ValidateToken",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("rpc.system", "grpc"),
			attribute.String("rpc.service", "AuthService"),
			attribute.String("rpc.method", "ValidateToken"),
		),
	)
	sleep(2, 8)
	auth.End()

	orderID := randomOrderID()

	// Create order (succeeds — will need compensation)
	ctx2, order := tracer("order-service").Start(ctx, "CreateOrder",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("order.id", orderID),
			attribute.String("order.status", "pending"),
			attribute.Float64("order.total", 100+rand.Float64()*900),
		),
	)
	sleep(15, 40)

	_, dbInsert := tracer("order-service").Start(ctx2, "INSERT orders",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "INSERT"),
			attribute.String("db.name", "orders_db"),
		),
	)
	sleep(5, 15)
	dbInsert.End()
	order.End()

	// Reserve inventory (succeeds — will need compensation)
	_, reserve := tracer("inventory-service").Start(ctx, "ReserveStock",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("order.id", orderID),
			attribute.Int("inventory.items", 1+rand.Intn(5)),
		),
	)
	sleep(15, 40)
	reserve.End()

	// Fraud check (borderline)
	_, fraud := tracer("fraud-service").Start(ctx, "AnalyzeTransaction",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("order.id", orderID),
			attribute.Float64("fraud.score", 0.3+rand.Float64()*0.3),
			attribute.String("fraud.decision", "review"),
		),
	)
	sleep(15, 40)
	fraud.End()

	// Payment — Stripe call with 3 retries, all fail
	ctx3, payment := tracer("payment-service").Start(ctx, "ProcessPayment",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("payment.provider", "stripe"),
			attribute.String("order.id", orderID),
		),
	)
	sleep(10, 25)

	for attempt := 1; attempt <= 3; attempt++ {
		_, retry := tracer("payment-service").Start(ctx3, fmt.Sprintf("POST https://api.stripe.com/v1/charges (attempt %d/3)", attempt),
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("http.method", "POST"),
				attribute.String("peer.service", "stripe-api"),
				attribute.Int("http.status_code", 402),
				attribute.Int("retry.attempt", attempt),
			),
		)
		sleep(60, 200)
		exc := randomException(paymentExceptions)
		recordException(retry, exc.excType, exc.message, exc.stacktrace)
		retry.End()
		if attempt < 3 {
			sleep(100*attempt, 200*attempt) // exponential backoff
		}
	}

	// Publish compensation event
	_, compensate := tracer("payment-service").Start(ctx3, "rabbitmq publish saga.compensate",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "publish"),
			attribute.String("messaging.destination", "saga.compensate"),
			attribute.String("saga.reason", "payment_failed"),
			attribute.String("order.id", orderID),
		),
	)
	sleep(2, 5)
	compensate.End()
	payment.SetStatus(codes.Error, "payment failed after 3 retries")
	payment.End()

	sleep(30, 100)
	if !consumersEnabled {
		return
	}

	// === COMPENSATION FAN-OUT (4 parallel compensating actions) ===
	compensations := make(chan struct{}, 4)

	// 1. Cancel order
	go func() {
		ctx4, cancel := tracer("order-service").Start(ctx, "CancelOrder (compensation)",
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("messaging.system", "rabbitmq"),
				attribute.String("saga.action", "cancel_order"),
				attribute.String("order.id", orderID),
			),
		)
		sleep(10, 30)
		_, dbCancel := tracer("order-service").Start(ctx4, "UPDATE orders SET status = 'cancelled'",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "postgresql"),
				attribute.String("db.operation", "UPDATE"),
				attribute.String("db.name", "orders_db"),
			),
		)
		sleep(5, 15)
		dbCancel.End()
		cancel.End()
		compensations <- struct{}{}
	}()

	// 2. Release inventory
	go func() {
		ctx4, release := tracer("inventory-service").Start(ctx, "ReleaseStock (compensation)",
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("messaging.system", "rabbitmq"),
				attribute.String("saga.action", "release_stock"),
				attribute.String("order.id", orderID),
			),
		)
		sleep(15, 40)
		_, dbRelease := tracer("inventory-service").Start(ctx4, "UPDATE inventory SET quantity = quantity + $1",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "postgresql"),
				attribute.String("db.operation", "UPDATE"),
				attribute.String("db.name", "inventory_db"),
			),
		)
		sleep(5, 15)
		dbRelease.End()
		release.End()
		compensations <- struct{}{}
	}()

	// 3. Void shipping label
	go func() {
		_, voidShip := tracer("shipping-service").Start(ctx, "VoidShippingLabel (compensation)",
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("messaging.system", "rabbitmq"),
				attribute.String("saga.action", "void_shipping"),
				attribute.String("order.id", orderID),
			),
		)
		sleep(20, 50)
		voidShip.End()
		compensations <- struct{}{}
	}()

	// 4. Notify user of cancellation -> email
	go func() {
		ctx4, notify := tracer("notification-service").Start(ctx, "SendOrderCancelledEmail",
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("messaging.system", "rabbitmq"),
				attribute.String("notification.type", "order_cancelled"),
				attribute.String("order.id", orderID),
			),
		)
		sleep(10, 25)
		_, email := tracer("email-service").Start(ctx4, "SendEmail",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("peer.service", "sendgrid"),
				attribute.String("email.template", "order_cancelled"),
			),
		)
		sleep(25, 80)
		email.End()
		notify.End()
		compensations <- struct{}{}
	}()

	for i := 0; i < 4; i++ {
		<-compensations
	}

	// Analytics event for cancellation
	_, analyticsCancel := tracer("analytics-service").Start(ctx, "TrackEvent order.cancelled",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", "analytics.events"),
			attribute.String("analytics.event_type", "order.cancelled"),
			attribute.String("order.id", orderID),
		),
	)
	sleep(2, 5)
	analyticsCancel.End()
}

// Timeout cascade — search-service is slow, gateway times out, frontend retries with cache fallback.
// Attempt 1: web -> gw -> search (SLOW, timeout) -> gw 504
// Attempt 2: web -> gw -> config (check feature flag) -> cache (stale hit) -> analytics
func timeoutCascadeFlow(ctx context.Context) {
	ctx, ui := tracer("web-frontend").Start(ctx, "SearchProducts",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.url", "/api/v2/search"),
			attribute.String("browser.page", "/search"),
			attribute.String("search.query", randomSearchTerm()),
		),
	)
	defer ui.End()
	sleep(2, 5)

	// First attempt — gateway times out
	_, gw1 := tracer("api-gateway").Start(ctx, "GET /api/v2/search (attempt 1/2)",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.route", "/api/v2/search"),
			attribute.Int("http.status_code", 504),
			attribute.Int("retry.attempt", 1),
		),
	)
	sleep(5, 10)

	// Search is slow — ES garbage collection or shard relocation
	_, slowSearch := tracer("search-service").Start(ctx, "Elasticsearch Query (slow)",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("db.system", "elasticsearch"),
			attribute.String("db.operation", "search"),
			attribute.Bool("timeout.exceeded", true),
		),
	)
	sleep(200, 500) // shortened for generator performance; attributes carry the semantics
	slowSearch.SetStatus(codes.Error, "request timed out after 30000ms")
	recordException(slowSearch, "Elasticsearch.Net.ElasticsearchClientException",
		"Request timed out after 30000ms",
		`Elasticsearch.Net.ElasticsearchClientException: Request timed out after 30000ms ---> System.OperationCanceledException
   at Elasticsearch.Net.HttpConnection.RequestAsync[TResponse](RequestData requestData)
   at SearchService.Search.ProductSearchService.SearchAsync(SearchQuery query) in /src/SearchService/Search/ProductSearchService.cs:line 71`)
	slowSearch.End()

	gw1.SetStatus(codes.Error, "upstream timeout after 30s")
	gw1.End()

	sleep(50, 200) // client retry delay

	// Second attempt — circuit breaker open, serves stale cache
	_, gw2 := tracer("api-gateway").Start(ctx, "GET /api/v2/search (attempt 2/2)",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.route", "/api/v2/search"),
			attribute.Int("http.status_code", 200),
			attribute.Int("retry.attempt", 2),
			attribute.Bool("circuit_breaker.open", true),
			attribute.String("circuit_breaker.fallback", "stale_cache"),
		),
	)
	sleep(3, 8)

	// Config check — stale cache fallback enabled?
	_, configCheck := tracer("config-service").Start(ctx, "GetFeatureFlag search.stale_cache_fallback",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("feature_flag.key", "search.stale_cache_fallback"),
			attribute.Bool("feature_flag.enabled", true),
		),
	)
	sleep(1, 3)
	configCheck.End()

	// Serve from stale cache
	_, cacheHit := tracer("cache-service").Start(ctx, "Redis GET search-cache (stale)",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "GET"),
			attribute.Bool("cache.hit", true),
			attribute.Bool("cache.stale", true),
			attribute.Int("cache.age_seconds", 300+rand.Intn(600)),
		),
	)
	sleep(1, 3)
	cacheHit.End()
	gw2.End()

	// Track degraded search
	_, analytics := tracer("analytics-service").Start(ctx, "TrackEvent search.degraded",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", "analytics.events"),
			attribute.String("analytics.event_type", "search.degraded"),
			attribute.String("degradation.reason", "elasticsearch_timeout"),
		),
	)
	sleep(2, 5)
	analytics.End()
}

// recordException adds an OTel exception event with realistic type, message, and stacktrace
func recordException(span trace.Span, excType, message, stacktrace string) {
	span.AddEvent("exception", trace.WithAttributes(
		attribute.String("exception.type", excType),
		attribute.String("exception.message", message),
		attribute.String("exception.stacktrace", stacktrace),
	))
	span.SetStatus(codes.Error, message)
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

// ─── GenAI Helper Functions ───────────────────────────────────────────────────
// These produce spans matching Microsoft Semantic Kernel / Agent Framework OTel
// output and OTel GenAI Semantic Conventions v1.38.0.

// chatSpan creates a "chat {model}" span on llm-gateway with standard gen_ai attributes.
func chatSpan(ctx context.Context, model string, inputTokens, outputTokens int, finishReasons ...string) (context.Context, trace.Span) {
	if len(finishReasons) == 0 {
		finishReasons = []string{"stop"}
	}
	ctx, span := tracer("llm-gateway").Start(ctx, "chat "+model,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.system", "openai"),
			attribute.String("gen_ai.request.model", model),
			attribute.String("gen_ai.response.model", model),
			attribute.Int("gen_ai.usage.input_tokens", inputTokens),
			attribute.Int("gen_ai.usage.output_tokens", outputTokens),
			attribute.StringSlice("gen_ai.response.finish_reasons", finishReasons),
			attribute.String("gen_ai.response.id", "chatcmpl-"+randomHex(24)),
			attribute.String("http.method", "POST"),
			attribute.String("http.url", "https://api.openai.com/v1/chat/completions"),
			attribute.String("peer.service", "openai-api"),
			attribute.Int("http.status_code", 200),
		),
	)
	return ctx, span
}

// embeddingSpan creates an "embedding {model}" span for text-to-vector operations.
func embeddingSpan(ctx context.Context, model string, inputTokens, dimensions int) (context.Context, trace.Span) {
	ctx, span := tracer("llm-gateway").Start(ctx, "embedding "+model,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "embedding"),
			attribute.String("gen_ai.system", "openai"),
			attribute.String("gen_ai.request.model", model),
			attribute.String("gen_ai.response.model", model),
			attribute.Int("gen_ai.usage.input_tokens", inputTokens),
			attribute.Int("gen_ai.embedding.dimension", dimensions),
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

// agentSpan creates an "invoke_agent {name}" span matching MS Agent Framework output.
func agentSpan(ctx context.Context, agentName, agentID, description, sessionID string) (context.Context, trace.Span) {
	ctx, span := tracer("ai-agent-service").Start(ctx, "invoke_agent "+agentName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.system", "openai"),
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
	_, embedSvc := tracer("embedding-service").Start(ctx, "GenerateQueryEmbedding",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("embedding.input_type", "query"),
			attribute.Int("embedding.text_length", len(query)),
		),
	)
	sleep(5, 15)

	inputTokens := 12 + rand.Intn(36)
	_, embedLLM := embeddingSpan(ctx, "text-embedding-3-small", inputTokens, 1536)
	sleep(80, 250)

	// Error: LLM rate limit -> fallback to text search
	if errorChance(0.05) {
		exc := randomException(llmExceptions)
		recordException(embedLLM, exc.excType, exc.message, exc.stacktrace)
		embedLLM.End()
		embedSvc.End()

		// Fallback to Elasticsearch text search
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
		recordException(vecSearch, exc.excType, exc.message, exc.stacktrace)
	}
	vecSearch.End()

	// LLM reranking
	rerankInputTokens := 800 + rand.Intn(1600)
	rerankOutputTokens := 150 + rand.Intn(250)
	_, rerank := chatSpan(ctx, "gpt-4o-mini", rerankInputTokens, rerankOutputTokens)
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
	ctx, agent := agentSpan(ctx, "CustomerSupportAgent", "csa-001", "Handles customer inquiries about orders and products", sessionID)
	defer agent.End()
	sleep(5, 15)

	// Planning step — LLM decides which tools to call
	planInputTokens := 200 + rand.Intn(600)
	planOutputTokens := 50 + rand.Intn(150)
	_, plan := chatSpan(ctx, "gpt-4o", planInputTokens, planOutputTokens, "tool_calls")
	plan.SetAttributes(
		attribute.Int("gen_ai.request.max_tokens", 1024),
		attribute.Float64("gen_ai.request.temperature", 0.1),
	)
	sleep(300, 800)

	// Error: hallucinated tool call
	if errorChance(0.04) {
		exc := randomException(agentExceptions)
		recordException(plan, exc.excType, exc.message, exc.stacktrace)
		plan.End()
		// Fallback response
		_, fallback := chatSpan(ctx, "gpt-4o", 100, 80)
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
	_, synth := chatSpan(ctx, "gpt-4o", synthInputTokens, synthOutputTokens)
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
		_, safety := chatSpan(ctx, "gpt-4o-mini", 100+rand.Intn(200), 30+rand.Intn(50))
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
		_, spam := chatSpan(ctx, "gpt-4o-mini", 80+rand.Intn(150), 20+rand.Intn(30))
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

		gateway.SetAttributes(attribute.Int("http.status_code", 202))

	case "block":
		// Content blocked
		exc := randomException(moderationExceptions)
		recordException(moderation, exc.excType, exc.message, exc.stacktrace)

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

	for i := 0; i < iterations; i++ {
		iterCtx := ctx

		// Plan step
		planInput := 200 + i*150 + rand.Intn(300) // grows with context
		planOutput := 50 + rand.Intn(100)
		totalInputTokens += planInput
		totalOutputTokens += planOutput

		_, plan := chatSpan(iterCtx, "gpt-4o", planInput, planOutput, "tool_calls")
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
			recordException(plan, exc.excType, exc.message, exc.stacktrace)
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
		_, reflect := chatSpan(iterCtx, "gpt-4o", reflectInput, reflectOutput, finishReason)
		reflect.SetAttributes(
			attribute.Float64("gen_ai.request.temperature", 0.2),
			attribute.Int("agent.iteration", i+1),
			attribute.String("agent.phase", "reflect"),
		)
		sleep(400, 1200)
		reflect.End()
	}

	// Set total token usage on agent span
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
}
