package main

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	logapi "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

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
	emitLog(ctx, "api-gateway", logapi.SeverityInfo, "Incoming request POST /api/v2/orders",
		logapi.String("http.method", "POST"), logapi.String("http.route", "/api/v2/orders"))
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
		recordException(ctx, "cache-service", cache, exc.excType, exc.message, exc.stacktrace)
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
		recordException(ctx2, "order-service", dbWrite, exc.excType, exc.message, exc.stacktrace)
	}
	dbWrite.End()
	emitLog(ctx2, "order-service", logapi.SeverityInfo, "Order created successfully",
		logapi.String("db.operation", "INSERT"))

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
	emitLog(ctx3, "payment-service", logapi.SeverityInfo, "Processing payment via Stripe",
		logapi.String("payment.provider", "stripe"))
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
	emitLog(ctx3, "payment-service", logapi.SeverityInfo, "Payment processed successfully")
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
		recordException(ctx2, "search-service", esQuery, exc.excType, exc.message, exc.stacktrace)
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
		recordException(ctx2, "auth-service", auth, exc.excType, exc.message, exc.stacktrace)
		emitLog(ctx2, "user-service", logapi.SeverityWarn, "Authentication failed for user")
		auth.End()
		return
	}
	emitLog(ctx2, "user-service", logapi.SeverityInfo, "User authenticated successfully")
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
	recordException(ctx, "api-gateway", gateway, exc.excType, exc.message, exc.stacktrace)
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
	recordException(ctx3, "payment-service", payment, exc.excType, exc.message, exc.stacktrace)
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
	recordException(ctx3, "payment-service", stripe, exc.excType, exc.message, exc.stacktrace)
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
			recordException(ctx, "notification-service", notify, exc.excType, exc.message, exc.stacktrace)
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
				recordException(ctx, "inventory-service", span, exc.excType, exc.message, exc.stacktrace)
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
		recordException(ctx2, "scheduler-service", dbQuery, exc.excType, exc.message, exc.stacktrace)
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
		recordException(ctx, "notification-service", notify, exc.excType, exc.message, exc.stacktrace)
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
		recordException(ctx, "auth-service", verify, exc.excType, exc.message, exc.stacktrace)
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
			recordException(ctx2, "search-service", searchSpan, exc.excType, exc.message, exc.stacktrace)
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
		recordException(ctx2, "product-service", productDB, exc.excType, exc.message, exc.stacktrace)
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
	emitLog(ctx, "api-gateway", logapi.SeverityInfo, "Checkout initiated",
		logapi.String("http.route", "/api/v2/checkout"))
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
			recordException(ctx, "shipping-service", ship, exc.excType, exc.message, exc.stacktrace)
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
		recordException(ctx3, "analytics-service", dbInsert, exc.excType, exc.message, exc.stacktrace)
	}
	dbInsert.End()
	order.End()

	// Fraud check with ML scoring
	emitLog(ctx, "fraud-service", logapi.SeverityInfo, "Running fraud analysis",
		logapi.String("order.id", orderID))
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
		recordException(ctx7, "email-service", email, exc.excType, exc.message, exc.stacktrace)
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
		emitLog(ctx3, "payment-service", logapi.SeverityWarn,
			fmt.Sprintf("Payment attempt %d/3 to Stripe", attempt),
			logapi.String("order.id", orderID))
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
		recordException(ctx3, "payment-service", retry, exc.excType, exc.message, exc.stacktrace)
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
	recordException(ctx, "search-service", slowSearch, "Elasticsearch.Net.ElasticsearchClientException",
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
