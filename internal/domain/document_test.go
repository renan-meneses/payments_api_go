package domain

import "testing"

func TestNormalizeDocument(t *testing.T) {
	cases := map[string]string{
		"529.982.247-25":     "52998224725",
		"11.222.333/0001-81": "11222333000181",
		" 123 456 789 09 ":   "12345678909",
	}
	for input, want := range cases {
		if got := NormalizeDocument(input); got != want {
			t.Errorf("NormalizeDocument(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsValidDocument(t *testing.T) {
	valid := []string{
		"52998224725",    // valid CPF
		"11222333000181", // valid CNPJ
	}
	for _, doc := range valid {
		if !IsValidDocument(doc) {
			t.Errorf("document %q should be valid", doc)
		}
	}

	invalid := []string{
		"",
		"52998224724",    // wrong check digit
		"11111111111",    // repeated digits
		"11222333000182", // wrong CNPJ check digit
		"1234567890123",  // 13 digits
	}
	for _, doc := range invalid {
		if IsValidDocument(doc) {
			t.Errorf("document %q should be invalid", doc)
		}
	}
}
