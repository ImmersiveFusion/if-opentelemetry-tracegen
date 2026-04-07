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
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	logapi "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

var tracerPool = map[string][]trace.Tracer{}
var loggerPool = map[string][]logapi.Logger{}
var providers []*sdktrace.TracerProvider
var logProviders []*sdklog.LoggerProvider

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

// noopLogger discards log records for services not in the current tier
var noopLogger = sdklog.NewLoggerProvider().Logger("")

// logger returns a random instance's logger for the given service.
func logger(svc string) logapi.Logger {
	if logsDisabled {
		return noopLogger
	}
	pool := loggerPool[svc]
	if len(pool) == 0 {
		return noopLogger
	}
	return pool[rand.Intn(len(pool))]
}

// emitLog emits a log record correlated with the current span context.
// The SDK automatically extracts trace_id/span_id from ctx.
func emitLog(ctx context.Context, svc string, severity logapi.Severity, body string, attrs ...logapi.KeyValue) {
	if logsDisabled {
		return
	}
	var rec logapi.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(severity)
	rec.SetBody(logapi.StringValue(body))
	if len(attrs) > 0 {
		rec.AddAttributes(attrs...)
	}
	logger(svc).Emit(ctx, rec)
}

// parsed headers shared by all providers
var otlpHeaders map[string]string

func newProvider(ctx context.Context, serviceName, endpoint, instanceID, hostName string) *sdktrace.TracerProvider {
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
	}
	if len(otlpHeaders) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(otlpHeaders))
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

	// Create a matching log provider for this service instance
	if !logsDisabled {
		logOpts := []otlploggrpc.Option{
			otlploggrpc.WithEndpoint(endpoint),
		}
		if len(otlpHeaders) > 0 {
			logOpts = append(logOpts, otlploggrpc.WithHeaders(otlpHeaders))
		}
		if insecureMode {
			logOpts = append(logOpts, otlploggrpc.WithInsecure())
		} else {
			logOpts = append(logOpts, otlploggrpc.WithTLSCredentials(credentials.NewTLS(&tls.Config{})))
		}
		logExporter, err := otlploggrpc.New(ctx, logOpts...)
		if err != nil {
			log.Fatalf("failed to create log exporter for %s: %v", serviceName, err)
		}
		lp := sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewSimpleProcessor(logExporter)),
			sdklog.WithResource(resource.NewWithAttributes(
				semconv.SchemaURL,
				semconv.ServiceNameKey.String(serviceName),
				attribute.String("service.instance.id", instanceID),
				attribute.String("host.name", hostName),
			)),
		)
		loggerPool[serviceName] = append(loggerPool[serviceName], lp.Logger(serviceName))
		logProviders = append(logProviders, lp)
	}

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

// logsDisabled — when true, no OTel log records are emitted
var logsDisabled bool

// errorChance scales a base error probability by the global error multiplier.
// Base rates are tuned for -errors=5 (moderate). At 0 = no errors, at 10 = ~2x base.
func errorChance(baseRate float64) bool {
	return rand.Float64() < baseRate*errorMultiplier
}

func main() {
	endpointFlag := flag.String("endpoint", "localhost:4317", "OTLP gRPC endpoint (host:port)")
	headersFlag := flag.String("headers", "", "OTLP headers as key=value pairs, comma-separated (e.g. \"api-key=SECRET,x-org=myorg\")")
	level := flag.Int("level", 1, "aggressiveness 1-10 (1=whisper, 10=SCREAM)")
	errors := flag.Int("errors", 0, "error rate 0-10 (0=none, 5=normal, 10=chaos)")
	noConsumers := flag.Bool("no-consumers", false, "disable all async consumers (messages published but never consumed)")
	insecureFlag := flag.Bool("insecure", false, "use plaintext gRPC (no TLS) for local backends")
	noAIBackendsFlag := flag.Bool("no-ai-backends", false, "disable all LLM/AI backends (AI spans emit errors)")
	aiOnlyFlag := flag.Bool("ai-only", false, "only run AI agentic scenarios")
	complexityFlag := flag.String("complexity", "normal", "topology complexity: light, normal, heavy")
	noLogsFlag := flag.Bool("no-logs", false, "disable OTel log record emission (traces only)")
	flag.Parse()
	consumersEnabled = !*noConsumers
	insecureMode = *insecureFlag
	noAIBackends = *noAIBackendsFlag
	aiOnly = *aiOnlyFlag
	complexity = *complexityFlag
	logsDisabled = *noLogsFlag
	if complexity != "light" && complexity != "normal" && complexity != "heavy" {
		log.Fatalf("invalid complexity %q — must be light, normal, or heavy", complexity)
	}

	endpoint := *endpointFlag

	// Parse headers: -headers flag takes precedence, then OTEL_EXPORTER_OTLP_HEADERS env var
	otlpHeaders = parseHeaders(*headersFlag)
	if len(otlpHeaders) == 0 {
		otlpHeaders = parseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
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
		newProvider(ctx, p.svc, endpoint, p.id, p.node)
	}
	defer func() {
		for _, tp := range providers {
			tp.Shutdown(context.Background())
		}
		for _, lp := range logProviders {
			lp.Shutdown(context.Background())
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
