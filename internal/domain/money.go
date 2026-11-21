package domain

import (
	"errors"
	"fmt"
)

// Currency is an ISO 4217 alphabetic code.
type Currency string

const CurrencyBRL Currency = "BRL"

// ErrCurrencyMismatch is returned when arithmetic involves different currencies.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// Money is an immutable monetary value expressed in minor units (centavos).
//
// Using integers instead of float64 is mandatory for financial systems: it
// eliminates binary floating point rounding drift.
type Money struct {
	AmountCents int64    `json:"amount_cents"`
	Currency    Currency `json:"currency"`
}

// NewMoney validates and constructs a Money value. Zero is allowed (useful for
// aggregate buckets) but negative values are not.
func NewMoney(cents int64, currency Currency) (Money, error) {
	if cents < 0 {
		return Money{}, NewValidationError("amount_cents", "must be greater than or equal to zero")
	}
	if currency == "" {
		currency = CurrencyBRL
	}
	if len(currency) != 3 {
		return Money{}, NewValidationError("currency", "must be a 3-letter ISO 4217 code")
	}
	return Money{AmountCents: cents, Currency: currency}, nil
}

// MustMoney is a helper for constants and seeds; it panics on invalid input.
func MustMoney(cents int64, currency Currency) Money {
	m, err := NewMoney(cents, currency)
	if err != nil {
		panic(err)
	}
	return m
}

// ZeroMoney returns a zero amount in the given currency.
func ZeroMoney(currency Currency) Money {
	return Money{AmountCents: 0, Currency: currency}
}

func (m Money) Add(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, ErrCurrencyMismatch
	}
	return Money{AmountCents: m.AmountCents + other.AmountCents, Currency: m.Currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, ErrCurrencyMismatch
	}
	if other.AmountCents > m.AmountCents {
		return Money{}, NewValidationError("amount", "resulting amount cannot be negative")
	}
	return Money{AmountCents: m.AmountCents - other.AmountCents, Currency: m.Currency}, nil
}

func (m Money) IsZero() bool     { return m.AmountCents == 0 }
func (m Money) IsPositive() bool { return m.AmountCents > 0 }

func (m Money) Equal(other Money) bool {
	return m.Currency == other.Currency && m.AmountCents == other.AmountCents
}

// Decimal renders the amount as a decimal string, e.g. "149.90".
func (m Money) Decimal() string {
	sign := ""
	cents := m.AmountCents
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

func (m Money) String() string {
	return fmt.Sprintf("%s %s", m.Currency, m.Decimal())
}
