package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/renan/payments-api/internal/config"
	"github.com/renan/payments-api/internal/domain"
	"github.com/renan/payments-api/internal/infrastructure/kafka"
	"github.com/renan/payments-api/pkg/logger"
)

func main() {
	if err := run(); err != nil {
		slog.Error("consumer stopped with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run starts a standalone Kafka consumer for asynchronous processing: audit
// trail, notification fan-out, data warehouse projection. Keeping it in its own
// binary means the API scales independently from event processing.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.Kafka.Enabled {
		return fmt.Errorf("KAFKA_ENABLED must be true to run the events consumer")
	}

	log := logger.New(cfg.LogLevel, cfg.Env)

	consumer := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers:  cfg.Kafka.Brokers,
		Topic:    cfg.Kafka.Topic,
		GroupID:  cfg.Kafka.ConsumerGroup,
		ClientID: cfg.Kafka.ClientID + "-consumer",
	}, log)
	defer func() { _ = consumer.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("event consumer listening",
		slog.String("topic", cfg.Kafka.Topic),
		slog.String("group", cfg.Kafka.ConsumerGroup),
	)

	return consumer.Run(ctx, func(ctx context.Context, event domain.Event) error {
		// Stub side effect. Real consumers would notify the merchant, append to
		// an immutable audit log or update a read projection.
		log.InfoContext(ctx, "domain event received",
			slog.String("event_id", event.ID),
			slog.String("event_type", event.Type),
			slog.String("aggregate_id", event.AggregateID),
			slog.Time("occurred_at", event.OccurredAt),
		)
		return nil
	})
}
