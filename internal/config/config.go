// Package config loads and validates the process configuration from the
// environment (12-factor). A .env file is loaded automatically in development
// without overriding real environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/renan/payments-api/internal/domain"
)

type Config struct {
	Env      string
	LogLevel string

	HTTP               HTTPConfig
	Postgres           PostgresConfig
	Redis              RedisConfig
	Kafka              KafkaConfig
	Auth               AuthConfig
	RateLimit          RateLimitConfig
	Payment            PaymentConfig
	Gateways           GatewayConfig
	Idempotency        IdempotencyConfig
	Dashboard          DashboardConfig
	AllowPixSimulation bool
}

type HTTPConfig struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	RequestTimeout  time.Duration
}

type PostgresConfig struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

type KafkaConfig struct {
	Brokers       []string
	Topic         string
	ClientID      string
	ConsumerGroup string
	Enabled       bool
}

type AuthConfig struct {
	// APIKeys maps an API key to a tenant identifier.
	APIKeys map[string]string
}

type RateLimitConfig struct {
	Requests int
	Window   time.Duration
	FailOpen bool
}

type PaymentConfig struct {
	CardProvider   domain.PaymentProvider
	PixProvider    domain.PaymentProvider
	PixExpiration  time.Duration
	MaxAmountCents int64
}

type GatewayConfig struct {
	Stripe      StripeConfig
	MercadoPago MercadoPagoConfig
	StaticPix   StaticPixConfig
}

type StripeConfig struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
}

type MercadoPagoConfig struct {
	AccessToken     string
	BaseURL         string
	WebhookSecret   string
	NotificationURL string
	Timeout         time.Duration
}

type StaticPixConfig struct {
	Key          string
	MerchantName string
	MerchantCity string
}

type IdempotencyConfig struct {
	TTL     time.Duration
	LockTTL time.Duration
}

type DashboardConfig struct {
	CacheTTL time.Duration
	MaxRange time.Duration
}

// Load reads the environment, applies defaults and validates the result.
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		Env:      envString("APP_ENV", "development"),
		LogLevel: envString("LOG_LEVEL", "info"),
		HTTP: HTTPConfig{
			Addr:            envString("HTTP_ADDR", ":8080"),
			ReadTimeout:     envDuration("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:    envDuration("HTTP_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:     envDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: envDuration("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
			RequestTimeout:  envDuration("HTTP_REQUEST_TIMEOUT", 12*time.Second),
		},
		Postgres: PostgresConfig{
			DSN:             envString("POSTGRES_DSN", ""),
			MaxConns:        int32(envInt("POSTGRES_MAX_CONNS", 20)),
			MinConns:        int32(envInt("POSTGRES_MIN_CONNS", 2)),
			MaxConnLifetime: envDuration("POSTGRES_MAX_CONN_LIFETIME", time.Hour),
		},
		Redis: RedisConfig{
			Addr:     envString("REDIS_ADDR", "localhost:6379"),
			Password: envString("REDIS_PASSWORD", ""),
			DB:       envInt("REDIS_DB", 0),
		},
		Kafka: KafkaConfig{
			Brokers:       envList("KAFKA_BROKERS"),
			Topic:         envString("KAFKA_TOPIC", "payments.events"),
			ClientID:      envString("KAFKA_CLIENT_ID", "payments-api"),
			ConsumerGroup: envString("KAFKA_CONSUMER_GROUP", "payments-audit"),
			Enabled:       envBool("KAFKA_ENABLED", true),
		},
		Auth: AuthConfig{APIKeys: parseAPIKeys(envString("API_KEYS", ""))},
		RateLimit: RateLimitConfig{
			Requests: envInt("RATE_LIMIT_REQUESTS", 120),
			Window:   envDuration("RATE_LIMIT_WINDOW", time.Minute),
			FailOpen: envBool("RATE_LIMIT_FAIL_OPEN", true),
		},
		Payment: PaymentConfig{
			CardProvider:   domain.PaymentProvider(strings.ToLower(envString("CARD_PROVIDER", "stripe"))),
			PixProvider:    domain.PaymentProvider(strings.ToLower(envString("PIX_PROVIDER", "static"))),
			PixExpiration:  envDuration("PIX_EXPIRATION", 30*time.Minute),
			MaxAmountCents: int64(envInt("MAX_AMOUNT_CENTS", 100_000_000)),
		},
		Gateways: GatewayConfig{
			Stripe: StripeConfig{
				APIKey:  envString("STRIPE_API_KEY", ""),
				BaseURL: envString("STRIPE_BASE_URL", "https://api.stripe.com"),
				Timeout: envDuration("STRIPE_TIMEOUT", 15*time.Second),
			},
			MercadoPago: MercadoPagoConfig{
				AccessToken:     envString("MERCADOPAGO_ACCESS_TOKEN", ""),
				BaseURL:         envString("MERCADOPAGO_BASE_URL", "https://api.mercadopago.com"),
				WebhookSecret:   envString("MERCADOPAGO_WEBHOOK_SECRET", ""),
				NotificationURL: envString("MERCADOPAGO_NOTIFICATION_URL", ""),
				Timeout:         envDuration("MERCADOPAGO_TIMEOUT", 15*time.Second),
			},
			StaticPix: StaticPixConfig{
				Key:          envString("STATIC_PIX_KEY", "dev-pix-key@example.com"),
				MerchantName: envString("STATIC_PIX_MERCHANT_NAME", "PAYMENTS API STORE"),
				MerchantCity: envString("STATIC_PIX_MERCHANT_CITY", "SAO PAULO"),
			},
		},
		Idempotency: IdempotencyConfig{
			TTL:     envDuration("IDEMPOTENCY_TTL", 24*time.Hour),
			LockTTL: envDuration("IDEMPOTENCY_LOCK_TTL", 45*time.Second),
		},
		Dashboard: DashboardConfig{
			CacheTTL: envDuration("DASHBOARD_CACHE_TTL", time.Minute),
			MaxRange: envDuration("DASHBOARD_MAX_RANGE", 366*24*time.Hour),
		},
		AllowPixSimulation: envBool("ALLOW_PIX_SIMULATION", false),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.Postgres.DSN) == "" {
		return fmt.Errorf("config: POSTGRES_DSN is required")
	}
	if len(c.Auth.APIKeys) == 0 {
		return fmt.Errorf("config: API_KEYS must contain at least one api-key:tenant pair")
	}
	if !c.Payment.CardProvider.IsValid() {
		return fmt.Errorf("config: unsupported CARD_PROVIDER %q", c.Payment.CardProvider)
	}
	if !c.Payment.PixProvider.IsValid() {
		return fmt.Errorf("config: unsupported PIX_PROVIDER %q", c.Payment.PixProvider)
	}
	if c.Payment.CardProvider == domain.ProviderStripe && c.Gateways.Stripe.APIKey == "" {
		return fmt.Errorf("config: STRIPE_API_KEY is required when CARD_PROVIDER=stripe")
	}
	if c.Payment.PixProvider == domain.ProviderMercadoPago && c.Gateways.MercadoPago.AccessToken == "" {
		return fmt.Errorf("config: MERCADOPAGO_ACCESS_TOKEN is required when PIX_PROVIDER=mercadopago")
	}
	if c.Payment.PixExpiration <= 0 {
		return fmt.Errorf("config: PIX_EXPIRATION must be positive")
	}
	if c.RateLimit.Requests <= 0 || c.RateLimit.Window <= 0 {
		return fmt.Errorf("config: rate limit must be positive")
	}
	if c.Idempotency.LockTTL <= 0 || c.Idempotency.TTL <= 0 {
		return fmt.Errorf("config: idempotency TTLs must be positive")
	}
	if c.Kafka.Enabled && len(c.Kafka.Brokers) == 0 {
		return fmt.Errorf("config: KAFKA_BROKERS is required when KAFKA_ENABLED=true")
	}
	return nil
}

func envString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envInt(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return v
}

func envList(key string) []string {
	raw := envString(key, "")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseAPIKeys(raw string) map[string]string {
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, tenant, found := strings.Cut(pair, ":")
		key, tenant = strings.TrimSpace(key), strings.TrimSpace(tenant)
		if !found || key == "" {
			continue
		}
		if tenant == "" {
			tenant = "default"
		}
		out[key] = tenant
	}
	return out
}
