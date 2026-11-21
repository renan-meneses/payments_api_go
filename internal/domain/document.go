package domain

import (
	"strings"
	"unicode"
)

// NormalizeDocument strips formatting characters so "123.456.789-09" and
// "12345678909" are treated as the same document.
func NormalizeDocument(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// IsValidCPF validates a Brazilian individual taxpayer number using its two
// check digits. Input must already be normalized to 11 digits.
func IsValidCPF(digits string) bool {
	if len(digits) != 11 || allSameDigit(digits) {
		return false
	}
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(digits[i]-'0') * (10 - i)
	}
	first := (sum * 10) % 11
	if first == 10 {
		first = 0
	}
	if first != int(digits[9]-'0') {
		return false
	}
	sum = 0
	for i := 0; i < 10; i++ {
		sum += int(digits[i]-'0') * (11 - i)
	}
	second := (sum * 10) % 11
	if second == 10 {
		second = 0
	}
	return second == int(digits[10]-'0')
}

// IsValidCNPJ validates a Brazilian company registration number.
func IsValidCNPJ(digits string) bool {
	if len(digits) != 14 || allSameDigit(digits) {
		return false
	}
	firstWeights := []int{5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}
	secondWeights := []int{6, 5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}

	sum := 0
	for i, w := range firstWeights {
		sum += int(digits[i]-'0') * w
	}
	first := sum % 11
	if first < 2 {
		first = 0
	} else {
		first = 11 - first
	}
	if first != int(digits[12]-'0') {
		return false
	}

	sum = 0
	for i, w := range secondWeights {
		sum += int(digits[i]-'0') * w
	}
	second := sum % 11
	if second < 2 {
		second = 0
	} else {
		second = 11 - second
	}
	return second == int(digits[13]-'0')
}

// IsValidDocument accepts either a CPF (11 digits) or a CNPJ (14 digits).
func IsValidDocument(normalized string) bool {
	switch len(normalized) {
	case 11:
		return IsValidCPF(normalized)
	case 14:
		return IsValidCNPJ(normalized)
	default:
		return false
	}
}

func allSameDigit(s string) bool {
	first := s[0]
	for i := 1; i < len(s); i++ {
		if s[i] != first {
			return false
		}
	}
	return true
}
