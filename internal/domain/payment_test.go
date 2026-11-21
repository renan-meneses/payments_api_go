package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testPixParams(now time.Time) NewPaymentParams {
	expiry := now.Add(30 * time.Minute)
	return NewPaymentParams{
		IdempotencyKey:      "idem-key-0001",
		ReceivingLocationID: uuid.New(),
		Amount:              MustMoney(14990, CurrencyBRL),
		Method:              PaymentMethodPix,
		Provider:            ProviderStatic,
		Customer:            Customer{Name: "Maria Silva", Document: "52998224725"},
		ExpiresAt:           &expiry,
	}
}

func TestNewPaymentValidatesPixRequirements(t *testing.T) {
	now := time.Now().UTC()

	t.Run("valid", func(t *testing.T) {
		payment, err := NewPayment(testPixParams(now), now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if payment.Status != PaymentStatusPending {
			t.Fatalf("status = %s, want pending", payment.Status)
		}
		if payment.Version != 1 {
			t.Fatalf("version = %d, want 1", payment.Version)
		}
	})

	t.Run("pix requires a document", func(t *testing.T) {
		params := testPixParams(now)
		params.Customer.Document = ""
		if _, err := NewPayment(params, now); err == nil {
			t.Fatal("expected a validation error")
		}
	})

	t.Run("pix requires a future expiration", func(t *testing.T) {
		params := testPixParams(now)
		past := now.Add(-time.Minute)
		params.ExpiresAt = &past
		if _, err := NewPayment(params, now); err == nil {
			t.Fatal("expected a validation error")
		}
	})

	t.Run("invalid idempotency key", func(t *testing.T) {
		params := testPixParams(now)
		params.IdempotencyKey = "short"
		_, err := NewPayment(params, now)
		if err == nil || !strings.Contains(err.Error(), "idempotency_key") {
			t.Fatalf("expected an idempotency key error, got %v", err)
		}
	})

	t.Run("zero amount", func(t *testing.T) {
		params := testPixParams(now)
		params.Amount = MustMoney(0, CurrencyBRL)
		if _, err := NewPayment(params, now); err == nil {
			t.Fatal("expected a validation error")
		}
	})
}

func TestMarkPaidIsIdempotent(t *testing.T) {
	now := time.Now().UTC()
	payment, err := NewPayment(testPixParams(now), now)
	if err != nil {
		t.Fatalf("NewPayment: %v", err)
	}

	paidAt := now.Add(time.Minute)
	if err := payment.MarkPaid("mp-123", "approved", paidAt); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}
	if payment.Status != PaymentStatusPaid || payment.PaidAt == nil {
		t.Fatal("payment must be paid")
	}

	// A replayed webhook must not fail and must not change the state.
	if err := payment.MarkPaid("mp-123", "approved", paidAt.Add(time.Hour)); err != nil {
		t.Fatalf("replayed MarkPaid returned an error: %v", err)
	}
	if payment.PaidAt == nil || !payment.PaidAt.Equal(paidAt) {
		t.Fatal("replayed MarkPaid must not move the paid timestamp")
	}
}

func TestInvalidStatusTransitions(t *testing.T) {
	now := time.Now().UTC()
	payment, err := NewPayment(testPixParams(now), now)
	if err != nil {
		t.Fatalf("NewPayment: %v", err)
	}

	if err := payment.MarkFailed("declined", "rejected", now); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	// Failed is terminal: a late approval must be rejected by the state machine.
	if err := payment.MarkPaid("mp-1", "approved", now); err == nil {
		t.Fatal("expected an invalid transition from failed to paid")
	}
}

func TestApplyRefund(t *testing.T) {
	now := time.Now().UTC()
	payment, err := NewPayment(testPixParams(now), now)
	if err != nil {
		t.Fatalf("NewPayment: %v", err)
	}
	if err := payment.MarkPaid("mp-1", "approved", now); err != nil {
		t.Fatalf("MarkPaid: %v", err)
	}

	if err := payment.ApplyRefund(MustMoney(5000, CurrencyBRL), now); err != nil {
		t.Fatalf("partial refund: %v", err)
	}
	if payment.Status != PaymentStatusPartiallyRefunded {
		t.Fatalf("status = %s, want partially_refunded", payment.Status)
	}

	if err := payment.ApplyRefund(MustMoney(9990, CurrencyBRL), now); err != nil {
		t.Fatalf("full refund: %v", err)
	}
	if payment.Status != PaymentStatusRefunded {
		t.Fatalf("status = %s, want refunded", payment.Status)
	}
	if payment.RefundedAmount.AmountCents != 14990 {
		t.Fatalf("refunded = %d, want 14990", payment.RefundedAmount.AmountCents)
	}

	if err := payment.ApplyRefund(MustMoney(100, CurrencyBRL), now); err == nil {
		t.Fatal("expected refund over the captured amount to fail")
	}
}

func TestAttachPixCharge(t *testing.T) {
	now := time.Now().UTC()
	payment, err := NewPayment(testPixParams(now), now)
	if err != nil {
		t.Fatalf("NewPayment: %v", err)
	}

	info := PixInfo{CopyPaste: "000201...6304ABCD", ExpiresAt: now.Add(30 * time.Minute)}
	if err := payment.AttachPixCharge("mp-42", "pending", info, now); err != nil {
		t.Fatalf("AttachPixCharge: %v", err)
	}
	if payment.Pix == nil || payment.Pix.CopyPaste != info.CopyPaste {
		t.Fatal("PIX data was not attached")
	}
	if payment.ProviderPaymentID != "mp-42" {
		t.Fatalf("provider payment id = %q", payment.ProviderPaymentID)
	}
}
