package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/renan/payments-api/internal/config"
	httpdelivery "github.com/renan/payments-api/internal/delivery/http"
	"github.com/renan/payments-api/internal/delivery/http/middleware"
	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/internal/infrastructure/gateway"
	"github.com/renan/payments-api/internal/infrastructure/gateway/mercadopago"
	"github.com/renan/payments-api/internal/infrastructure/gateway/staticpix"
	"github.com/renan/payments-api/internal/infrastructure/gateway/stripe"
	"github.com/renan/payments-api/internal/infrastructure/idempotency"
	"github.com/renan/payments-api/internal/infrastructure/kafka"
	"github.com/renan/payments-api/internal/infrastructure/postgres"
	redisinfra "github.com/renan/payments-api/internal/infrastructure/redis"
	"github.com/renan/payments-api/internal/usecase"
	"github.com/renan/payments-api/pkg/clock"
	"github.com/renan/payments-api/pkg/logger"
)

const maxIdempotentResponseBytes = 512 * 1024

func main() {
	if err := run(); err != nil {
		slog.Error("application stopped with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run is the composition root: it constructs every adapter, injects them into
// the use cases and starts the HTTP server. No business logic lives here.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logger.New(cfg.LogLevel, cfg.Env)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, postgres.Config{
		DSN:             cfg.Postgres.DSN,
		MaxConns:        cfg.Postgres.MaxConns,
		MinConns:        cfg.Postgres.MinConns,
		MaxConnLifetime: cfg.Postgres.MaxConnLifetime,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	redisClient, err := redisinfra.NewClient(ctx, redisinfra.Config{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	if err != nil {
		return err
	}
	defer func() { _ = redisClient.Close() }()

	// ----------------------------- Messaging --------------------------------
	var publisher usecase.EventPublisher
	if cfg.Kafka.Enabled {
		producer := kafka.NewProducer(kafka.ProducerConfig{
			Brokers:  cfg.Kafka.Brokers,
			Topic:    cfg.Kafka.Topic,
			ClientID: cfg.Kafka.ClientID,
		}, log)
		defer func() { _ = producer.Close() }()
		publisher = producer
	} else {
		log.Warn("Kafka publishing is disabled (KAFKA_ENABLED=false)")
		publisher = kafka.NewNoopPublisher(log)
	}

	// ----------------------------- Repositories ----------------------------
	paymentRepo := postgres.NewPaymentRepository(pool)
	locationRepo := postgres.NewReceivingLocationRepository(pool)
	webhookRepo := postgres.NewWebhookEventRepository(pool)
	dashboardRepo := postgres.NewDashboardRepository(pool)
	txManager := postgres.NewTxManager(pool)

	// ----------------------------- Adapters --------------------------------
	clk := clock.System{}
	idemStore := redisinfra.NewIdempotencyStore(redisClient, cfg.Idempotency.LockTTL, cfg.Idempotency.TTL)
	idemGuard := idempotency.NewGuard(idemStore, postgres.NewIdempotencyLogRepository(pool), cfg.Idempotency.TTL, log)
	dashboardCache := redisinfra.NewDashboardCache(redisClient, cfg.Dashboard.CacheTTL, log)
	rateLimiter := redisinfra.NewRateLimiter(redisClient, cfg.RateLimit.Requests, cfg.RateLimit.Window, log)

	gateways := gateway.NewRegistry()
	var webhookParsers []usecase.WebhookParser

	switch cfg.Payment.CardProvider {
	case domain.ProviderStripe:
		gateways.RegisterCard(stripe.New(stripe.Config{
			APIKey:  cfg.Gateways.Stripe.APIKey,
			BaseURL: cfg.Gateways.Stripe.BaseURL,
			Timeout: cfg.Gateways.Stripe.Timeout,
		}))
	default:
		return fmt.Errorf("no card gateway adapter for provider %q", cfg.Payment.CardProvider)
	}

	switch cfg.Payment.PixProvider {
	case domain.ProviderMercadoPago:
		mpClient := mercadopago.New(mercadopago.Config{
			AccessToken:     cfg.Gateways.MercadoPago.AccessToken,
			BaseURL:         cfg.Gateways.MercadoPago.BaseURL,
			WebhookSecret:   cfg.Gateways.MercadoPago.WebhookSecret,
			NotificationURL: cfg.Gateways.MercadoPago.NotificationURL,
			Timeout:         cfg.Gateways.MercadoPago.Timeout,
		})
		gateways.RegisterPix(mpClient)
		webhookParsers = append(webhookParsers, mercadopago.NewWebhookParser(mpClient, clk))
	case domain.ProviderStatic:
		log.Warn("using the static PIX gateway: BR Codes are generated locally and cannot be paid")
		gateways.RegisterPix(staticpix.New(staticpix.Config{
			PixKey:       cfg.Gateways.StaticPix.Key,
			MerchantName: cfg.Gateways.StaticPix.MerchantName,
			MerchantCity: cfg.Gateways.StaticPix.MerchantCity,
		}))
	default:
		return fmt.Errorf("no pix gateway adapter for provider %q", cfg.Payment.PixProvider)
	}

	// ----------------------------- Use cases --------------------------------
	processPayment := usecase.NewProcessPayment(usecase.ProcessPaymentDeps{
		Payments:       paymentRepo,
		Locations:      locationRepo,
		CardGateways:   gateways,
		PixGateways:    gateways,
		Publisher:      publisher,
		DashboardCache: dashboardCache,
		Clock:          clk,
		Logger:         log,
		Config: usecase.ProcessPaymentConfig{
			CardProvider:   cfg.Payment.CardProvider,
			PixProvider:    cfg.Payment.PixProvider,
			PixExpiration:  cfg.Payment.PixExpiration,
			MaxAmountCents: cfg.Payment.MaxAmountCents,
		},
	})

	handlePixWebhook := usecase.NewHandlePixWebhook(usecase.HandlePixWebhookDeps{
		Payments:       paymentRepo,
		Webhooks:       webhookRepo,
		Tx:             txManager,
		Publisher:      publisher,
		DashboardCache: dashboardCache,
		Clock:          clk,
		Logger:         log,
	})

	getPayment := usecase.NewGetPayment(usecase.GetPaymentDeps{
		Payments:  paymentRepo,
		Locations: locationRepo,
		Logger:    log,
	})

	getDashboard := usecase.NewGetDashboardSummary(usecase.GetDashboardSummaryDeps{
		Repository: dashboardRepo,
		Cache:      dashboardCache,
		Clock:      clk,
		Logger:     log,
		Config: usecase.DashboardConfig{
			CacheTTL: cfg.Dashboard.CacheTTL,
			MaxRange: cfg.Dashboard.MaxRange,
		},
	})

	// ----------------------------- Delivery ---------------------------------
	health := httpdelivery.NewHealthHandler(map[string]httpdelivery.Check{
		"postgres": func(ctx context.Context) error { return pool.Ping(ctx) },
		"redis":    func(ctx context.Context) error { return redisClient.Ping(ctx).Err() },
	})

	router := httpdelivery.NewRouter(httpdelivery.RouterDependencies{
		Logger: log,
		Config: httpdelivery.RouterConfig{
			RequestTimeout:        cfg.HTTP.RequestTimeout,
			RateLimitFailOpen:     cfg.RateLimit.FailOpen,
			IdempotencyMaxCapture: maxIdempotentResponseBytes,
		},
		Payments:    httpdelivery.NewPaymentHandler(processPayment, getPayment, log),
		Dashboard:   httpdelivery.NewDashboardHandler(getDashboard, log),
		Webhooks:    httpdelivery.NewWebhookHandler(handlePixWebhook, webhookParsers, log, cfg.AllowPixSimulation),
		Health:      health,
		Auth:        middleware.NewAuthenticator(cfg.Auth.APIKeys),
		RateLimiter: rateLimiter,
		Idempotency: idemGuard,
	})

	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	serverErrors := make(chan error, 1)
	go func() {
		log.Info("http server listening", slog.String("addr", cfg.HTTP.Addr), slog.String("env", cfg.Env))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	select {
	case err := <-serverErrors:
		return fmt.Errorf("http server failed: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	log.Info("server stopped cleanly")
	return nil
}
