package brcode

import (
	"fmt"
	"strings"
	"testing"
)

// Known CRC-16/CCITT-FALSE check value for "123456789".
func TestCRC16CCITTKnownVector(t *testing.T) {
	if got, want := CRC16CCITT("123456789"), uint16(0x29B1); got != want {
		t.Fatalf("CRC16CCITT = %#04x, want %#04x", got, want)
	}
}

func TestBuildStaticProducesValidCRC(t *testing.T) {
	payload, err := BuildStatic(StaticInput{
		PixKey:       "loja@example.com",
		MerchantName: "Loja São João",
		MerchantCity: "São Paulo",
		AmountCents:  14990,
		TxID:         "ORDER-123",
	})
	if err != nil {
		t.Fatalf("BuildStatic: %v", err)
	}

	if !strings.HasPrefix(payload, "000201") {
		t.Fatalf("payload must start with the format indicator, got %q", payload[:6])
	}
	if len(payload) < 8 {
		t.Fatalf("payload too short: %q", payload)
	}

	body := payload[:len(payload)-4]
	want := fmt.Sprintf("%04X", CRC16CCITT(body))
	if got := payload[len(payload)-4:]; got != want {
		t.Fatalf("trailing CRC = %s, want %s", got, want)
	}
	if !strings.Contains(payload, "5406149.90") {
		t.Fatalf("amount TLV missing: %s", payload)
	}
	if !strings.Contains(payload, "LOJA SAO JOAO") {
		t.Fatalf("sanitized merchant name missing: %s", payload)
	}
}

func TestBuildStaticRejectsEmptyKey(t *testing.T) {
	if _, err := BuildStatic(StaticInput{}); err == nil {
		t.Fatal("expected an error for a missing pix key")
	}
}
