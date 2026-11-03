package usecases

import "testing"

func TestSanitizeLogStripsLineControlCharacters(t *testing.T) {
	got := sanitizeLog("re\rason\ninjected\r\nok")
	if want := "reasoninjectedok"; got != want {
		t.Fatalf("sanitizeLog = %q, want %q", got, want)
	}
	if got := sanitizeLog("plain"); got != "plain" {
		t.Fatalf("sanitizeLog altered a clean value: %q", got)
	}
}
