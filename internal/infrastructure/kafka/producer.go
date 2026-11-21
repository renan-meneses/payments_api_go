// Package kafka implements the EventPublisher port using segmentio/kafka-go.
package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/renan/payments-api/internal/domain"
)

type ProducerConfig struct {
	Brokers  []string
	Topic    string
	ClientID string
}

// Producer publishes domain events. It is synchronous with acks=all: the use
// case only learns that a payment was accepted after the event is durably
// replicated (required for financial auditability).
type Producer struct {
	writer *kafka.Writer
	topic  string
	logger *slog.Logger
}

func NewProducer(cfg ProducerConfig, logger *slog.Logger) *Producer {
	writer := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers...),
		Topic:                  cfg.Topic,
		Balancer:               &kafka.Hash{}, // partition by aggregate id => per-payment ordering
		RequiredAcks:           kafka.RequireAll,
		Async:                  false,
		BatchSize:              100,
		BatchTimeout:           5 * time.Millisecond,
		Compression:            kafka.Snappy,
		MaxAttempts:            3,
		WriteBackoffMin:        100 * time.Millisecond,
		WriteBackoffMax:        time.Second,
		AllowAutoTopicCreation: false,
	}
	return &Producer{writer: writer, topic: cfg.Topic, logger: logger}
}

func (p *Producer) Publish(ctx context.Context, events ...domain.Event) error {
	if len(events) == 0 {
		return nil
	}

	messages := make([]kafka.Message, 0, len(events))
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("encoding event %s: %w", event.Type, err)
		}
		messages = append(messages, kafka.Message{
			Key:   []byte(event.AggregateID),
			Value: payload,
			Time:  event.OccurredAt,
			Headers: []kafka.Header{
				{Key: "event-id", Value: []byte(event.ID)},
				{Key: "event-type", Value: []byte(event.Type)},
				{Key: "schema-version", Value: []byte("1")},
			},
		})
	}

	if err := p.writer.WriteMessages(ctx, messages...); err != nil {
		return fmt.Errorf("publishing %d event(s) to %s: %w", len(messages), p.topic, err)
	}
	return nil
}

func (p *Producer) Close() error {
	return p.writer.Close()
}

// NoopPublisher is used when Kafka is disabled (local unit tests, smoke runs).
// It keeps the composition root free of nil checks.
type NoopPublisher struct {
	logger *slog.Logger
}

func NewNoopPublisher(logger *slog.Logger) *NoopPublisher {
	return &NoopPublisher{logger: logger}
}

func (p *NoopPublisher) Publish(_ context.Context, events ...domain.Event) error {
	for _, event := range events {
		p.logger.Debug("event publisher disabled; dropping event",
			slog.String("event_type", event.Type),
			slog.String("aggregate_id", event.AggregateID),
		)
	}
	return nil
}
