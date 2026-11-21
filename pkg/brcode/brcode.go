// Package brcode builds Brazilian PIX "BR Code" payloads as defined by the
// Banco Central do Brasil EMV® QRCPS-MPM specification.
//
// The payload is the string customers paste into their banking app
// ("copia e cola"). It is a TLV (tag-length-value) structure terminated by a
// CRC16 checksum.
package brcode

import (
	"errors"
	"fmt"
	"strings"
)

// StaticInput describes a PIX charge. Static BR Codes carry the merchant's PIX
// key directly; dynamic ones (cob) carry a URL instead and are produced by the
// PSP (Mercado Pago) adapter.
type StaticInput struct {
	PixKey       string
	MerchantName string
	MerchantCity string
	AmountCents  int64  // 0 means "customer chooses the amount"
	TxID         string // alphanumeric, max 25 chars; empty becomes "***"
	Description  string
}

// BuildStatic renders a complete, checksum-terminated BR Code payload.
func BuildStatic(in StaticInput) (string, error) {
	pixKey := strings.TrimSpace(in.PixKey)
	if pixKey == "" {
		return "", errors.New("brcode: pix key is required")
	}
	if len(pixKey) > 77 {
		return "", errors.New("brcode: pix key exceeds 77 characters")
	}

	name := sanitize(in.MerchantName, 25)
	if name == "" {
		name = "MERCHANT"
	}
	city := sanitize(in.MerchantCity, 15)
	if city == "" {
		city = "SAO PAULO"
	}

	merchantAccount := emv("00", "br.gov.bcb.pix") + emv("01", pixKey)
	if desc := sanitize(in.Description, 72); desc != "" {
		merchantAccount += emv("02", desc)
	}

	txID := sanitizeTxID(in.TxID)

	var b strings.Builder
	b.WriteString(emv("00", "01")) // payload format indicator
	b.WriteString(emv("26", merchantAccount))
	b.WriteString(emv("52", "0000")) // merchant category code
	b.WriteString(emv("53", "986"))  // BRL
	if in.AmountCents > 0 {
		b.WriteString(emv("54", formatAmount(in.AmountCents)))
	}
	b.WriteString(emv("58", "BR"))
	b.WriteString(emv("59", name))
	b.WriteString(emv("60", city))
	b.WriteString(emv("62", emv("05", txID))) // additional data: txid

	withoutCRC := b.String() + "6304"
	return withoutCRC + fmt.Sprintf("%04X", CRC16CCITT(withoutCRC)), nil
}

// emv encodes a single tag-length-value field. Length is the byte length of the
// value, zero padded to two digits.
func emv(tag, value string) string {
	if len(value) > 99 {
		value = value[:99]
	}
	return fmt.Sprintf("%s%02d%s", tag, len(value), value)
}

// CRC16CCITT implements CRC-16/CCITT-FALSE (poly 0x1021, init 0xFFFF, no
// reflection, no final XOR), the checksum mandated by the PIX spec.
func CRC16CCITT(payload string) uint16 {
	var crc uint16 = 0xFFFF
	for i := 0; i < len(payload); i++ {
		crc ^= uint16(payload[i]) << 8
		for bit := 0; bit < 8; bit++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func formatAmount(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// sanitizeTxID keeps only [A-Za-z0-9], truncates to 25 and defaults to "***".
// The spec forbids a txid of "*" alone, hence the default.
func sanitizeTxID(txID string) string {
	var b strings.Builder
	for _, r := range txID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() == 25 {
			break
		}
	}
	if b.Len() == 0 {
		return "***"
	}
	return b.String()
}

var accentReplacer = strings.NewReplacer(
	"á", "A", "à", "A", "â", "A", "ã", "A", "ä", "A",
	"Á", "A", "À", "A", "Â", "A", "Ã", "A", "Ä", "A",
	"é", "E", "è", "E", "ê", "E", "ë", "E",
	"É", "E", "È", "E", "Ê", "E", "Ë", "E",
	"í", "I", "ì", "I", "î", "I", "ï", "I",
	"Í", "I", "Ì", "I", "Î", "I", "Ï", "I",
	"ó", "O", "ò", "O", "ô", "O", "õ", "O", "ö", "O",
	"Ó", "O", "Ò", "O", "Ô", "O", "Õ", "O", "Ö", "O",
	"ú", "U", "ù", "U", "û", "U", "ü", "U",
	"Ú", "U", "Ù", "U", "Û", "U", "Ü", "U",
	"ç", "C", "Ç", "C", "ñ", "N", "Ñ", "N",
)

// sanitize normalizes to uppercase ASCII and truncates to max characters.
func sanitize(value string, max int) string {
	value = accentReplacer.Replace(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if r < 0x20 || r > 0x7E {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= max {
			break
		}
	}
	return strings.ToUpper(b.String())
}
