package main

import (
	"context"
	"crypto/tls"
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

// tracer returns a random instance's tracer for the given service (multi-pod realism)
func tracer(svc string) trace.Tracer {
	pool := tracerPool[svc]
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
	flag.Parse()
	consumersEnabled = !*noConsumers
	insecureMode = *insecureFlag

	endpoint := *endpointFlag
	apiKey := *apiKeyFlag
	if apiKey == "" {
		// Check environment variable as fallback
		apiKey = os.Getenv("OTEL_APIKEY")
	}
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: API key required. Use -apikey flag or set OTEL_APIKEY environment variable.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  tracegen -apikey YOUR_KEY [-level N] [-errors N] [-no-consumers]")
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
	errorLabels := []string{"none", "rare", "rare", "low", "low", "normal", "elevated", "high", "high", "extreme", "chaos"}
	fmt.Printf("Endpoint: %s\n", endpoint)
	fmt.Printf("Level %d: %s  (tick=%dms, burst=%d-%d)  Errors: %s (%d)\n",
		*level, cfg.label, cfg.tickMs, cfg.burstMin, cfg.burstMax, errorLabels[*errors], *errors)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Create providers per service instance — realistic multi-pod deployment
	type pod struct{ svc, id, node string }
	pods := []pod{
		// web-frontend (2 replicas)
		{"web-frontend", "web-frontend-d7b48c-x2km9", "aks-userpool1-38437823-vmss000000"},
		{"web-frontend", "web-frontend-d7b48c-n3pr7", "aks-userpool1-38437823-vmss000001"},
		// api-gateway (3 replicas, spread across node pools)
		{"api-gateway", "api-gateway-e5a21b-j4hk8", "aks-userpool1-38437823-vmss000000"},
		{"api-gateway", "api-gateway-e5a21b-w2tp6", "aks-userpool1-38437823-vmss000001"},
		{"api-gateway", "api-gateway-e5a21b-m9qv1", "aks-userpool2-52891647-vmss000000"},
		// order-service (3 replicas)
		{"order-service", "order-svc-f3c91a-h5rd3", "aks-userpool1-38437823-vmss000001"},
		{"order-service", "order-svc-f3c91a-k8nj2", "aks-userpool1-38437823-vmss000002"},
		{"order-service", "order-svc-f3c91a-p7tw4", "aks-userpool2-52891647-vmss000000"},
		// payment-service (2 replicas)
		{"payment-service", "payment-svc-a82d4e-t7mw4", "aks-userpool1-38437823-vmss000002"},
		{"payment-service", "payment-svc-a82d4e-q1vx6", "aks-userpool2-52891647-vmss000001"},
		// inventory-service (2 replicas)
		{"inventory-service", "inventory-svc-b46e5f-k2pn8", "aks-userpool1-38437823-vmss000000"},
		{"inventory-service", "inventory-svc-b46e5f-j9dr4", "aks-userpool2-52891647-vmss000000"},
		// notification-service (2 replicas)
		{"notification-service", "notif-svc-c59f2a-m3wh7", "aks-userpool1-38437823-vmss000001"},
		{"notification-service", "notif-svc-c59f2a-x6kv1", "aks-userpool2-52891647-vmss000001"},
		// user-service (2 replicas)
		{"user-service", "user-svc-d63a7b-p4td2", "aks-userpool1-38437823-vmss000002"},
		{"user-service", "user-svc-d63a7b-n8jq5", "aks-userpool2-52891647-vmss000000"},
		// cache-service (3 replicas — Redis cluster)
		{"cache-service", "cache-svc-e71b4c-h6wm3", "aks-userpool1-38437823-vmss000000"},
		{"cache-service", "cache-svc-e71b4c-k9tp1", "aks-userpool1-38437823-vmss000001"},
		{"cache-service", "cache-svc-e71b4c-r2vd7", "aks-userpool2-52891647-vmss000001"},
		// search-service (2 replicas)
		{"search-service", "search-svc-f85c3d-j5mw2", "aks-userpool1-38437823-vmss000002"},
		{"search-service", "search-svc-f85c3d-t8nk4", "aks-userpool2-52891647-vmss000000"},
		// scheduler-service (1 replica — singleton cron)
		{"scheduler-service", "scheduler-svc-a93d1e-n1ph6", "aks-userpool1-38437823-vmss000000"},
		// auth-service (3 replicas — high traffic)
		{"auth-service", "auth-svc-b47e2f-m4kr8", "aks-userpool1-38437823-vmss000001"},
		{"auth-service", "auth-svc-b47e2f-w2jt9", "aks-userpool1-38437823-vmss000002"},
		{"auth-service", "auth-svc-b47e2f-p6hn3", "aks-userpool2-52891647-vmss000000"},
		// recommendation-service (2 replicas)
		{"recommendation-service", "rec-svc-c58a3c-h7vn2", "aks-userpool1-38437823-vmss000001"},
		{"recommendation-service", "rec-svc-c58a3c-p5tk8", "aks-userpool2-52891647-vmss000001"},
		// cart-service (2 replicas)
		{"cart-service", "cart-svc-d72b5e-j3kn9", "aks-userpool1-38437823-vmss000000"},
		{"cart-service", "cart-svc-d72b5e-w8pm4", "aks-userpool2-52891647-vmss000001"},
		// product-service (3 replicas — high read traffic)
		{"product-service", "product-svc-e85c6f-m2hr7", "aks-userpool1-38437823-vmss000000"},
		{"product-service", "product-svc-e85c6f-n9tv5", "aks-userpool1-38437823-vmss000001"},
		{"product-service", "product-svc-e85c6f-k4jw3", "aks-userpool2-52891647-vmss000000"},
		// shipping-service (2 replicas)
		{"shipping-service", "shipping-svc-f96d7a-p5tn2", "aks-userpool1-38437823-vmss000002"},
		{"shipping-service", "shipping-svc-f96d7a-h8km6", "aks-userpool2-52891647-vmss000000"},
		// fraud-service (2 replicas — ML model)
		{"fraud-service", "fraud-svc-a17e8b-j6wr4", "aks-userpool1-38437823-vmss000002"},
		{"fraud-service", "fraud-svc-a17e8b-m3tp1", "aks-userpool2-52891647-vmss000001"},
		// email-service (2 replicas)
		{"email-service", "email-svc-b28f9c-k4hn7", "aks-userpool1-38437823-vmss000001"},
		{"email-service", "email-svc-b28f9c-w9jm5", "aks-userpool2-52891647-vmss000000"},
		// tax-service (1 replica — lightweight)
		{"tax-service", "tax-svc-c39a1d-n2vp8", "aks-userpool1-38437823-vmss000000"},
		// analytics-service (3 replicas — high write volume)
		{"analytics-service", "analytics-svc-d41b2e-h5km3", "aks-userpool1-38437823-vmss000001"},
		{"analytics-service", "analytics-svc-d41b2e-p8jt6", "aks-userpool1-38437823-vmss000002"},
		{"analytics-service", "analytics-svc-d41b2e-w2nr9", "aks-userpool2-52891647-vmss000000"},
		// config-service (1 replica — singleton)
		{"config-service", "config-svc-e52c3f-m7wk4", "aks-userpool1-38437823-vmss000002"},
	}
	for _, p := range pods {
		newProvider(ctx, p.svc, endpoint, apiKey, p.id, p.node)
	}
	defer func() {
		for _, tp := range providers {
			tp.Shutdown(context.Background())
		}
	}()

	fmt.Println("Generating distributed traces... (Ctrl+C to stop)")

	ticker := time.NewTicker(time.Duration(cfg.tickMs) * time.Millisecond)
	defer ticker.Stop()

	type namedScenario struct {
		name    string
		fn      func(context.Context)
		isError bool // true = dedicated error scenario, excluded when -errors=0
	}
	allScenarios := []namedScenario{
		{"Create Order", createOrderFlow, false},
		{"Search & Browse", searchAndBrowseFlow, false},
		{"User Login", userLoginFlow, false},
		{"Failed Payment", failedPaymentFlow, true},
		{"Bulk Notifications", bulkNotificationFlow, false},
		{"Health Check", healthCheckFlow, false},
		{"Inventory Sync", inventorySyncFlow, false},
		{"Scheduled Report", scheduledReportFlow, false},
		{"Stripe Webhook", stripeWebhookFlow, false},
		{"Recommendations", recommendationFlow, false},
		{"Add to Cart", addToCartFlow, false},
		{"Full Checkout", fullCheckoutFlow, false},
		{"Shipping Update", shippingUpdateFlow, false},
		{"Saga Compensation", sagaCompensationFlow, true},
		{"Timeout Cascade", timeoutCascadeFlow, true},
	}
	var scenarios []namedScenario
	for _, s := range allScenarios {
		if s.isError && errorMultiplier == 0 {
			continue
		}
		scenarios = append(scenarios, s)
	}

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
					fmt.Printf("[%s] %d traces sent  (%d services, %d pods)\n", time.Now().Format("15:04:05"), sent, len(tracerPool), len(pods))
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
	terms := []string{"widget", "dashboard", "monitor", "alert rules", "trace analysis", "service health"}
	return terms[rand.Intn(len(terms))]
}
