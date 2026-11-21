package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/renan/payments-api/internal/domain"
)

type ConsumerConfig struct {
	Brokers  []string
	Topic    string
	GroupID  string
	ClientID string
}

// Consumer reads domain events with at-least-once semantics: the offset is
// committed only after the handler succeeds.
type Consumer struct {
	reader *kafka.Reader
	logger *slog.Logger
}

func NewConsumer(cfg ConsumerConfig, logger *slog.Logger) *Consumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        cfg.Brokers,
		GroupID:        cfg.GroupID,
		Topic:          cfg.Topic,
		MinBytes:       10e3, // 10 KiB
		MaxBytes:       10e6, // 10 MiB
		MaxWait:        time.Second,
		StartOffset:    kafka.FirstOffset,
		CommitInterval: 0, // manual commits only
	})
	return &Consumer{reader: reader, logger: logger}
}

// Handler processes one event. Returning an error leaves the offset uncommitted
// so the event is redelivered.
type Handler func(ctx context.Context, event domain.Event) error

func (c *Consumer) Run(ctx context.Context, handler Handler) error {
	for {
		message, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("fetching kafka message: %w", err)
		}

		var event domain.Event
		if err := json.Unmarshal(message.Value, &event); err != nil {
			// Poison message: log, commit and move on. Retrying forever would
			// block the partition.
			c.logger.Error("discarding malformed domain event",
				slog.String("topic", message.Topic),
				slog.Int("partition", message.Partition),
				slog.Int64("offset", message.Offset),
				slog.String("error", err.Error()),
			)
			if err := c.reader.CommitMessages(ctx, message); err != nil {
				return fmt.Errorf("committing poison message: %w", err)
			}
			continue
		}

		if err := handler(ctx, event); err != nil {
			c.logger.Error("domain event handler failed; will retry",
				slog.String("event_id", event.ID),
				slog.String("event_type", event.Type),
				slog.String("error", err.Error()),
			)
			// Back off briefly to avoid a hot redelivery loop.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}

		if err := c.reader.CommitMessages(ctx, message); err != nil {
			return fmt.Errorf("committing kafka message: %w", err)
		}
	}
}

func (c *Consumer) Close() error {
	return c.reader.Close()
}
