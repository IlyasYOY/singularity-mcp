package singularity

import (
	"strings"
	"testing"
)

func TestSafeSnippetRedactsTokenAcrossTruncationBoundary(t *testing.T) {
	const token = "secret-token-crossing-boundary"
	body := []byte(strings.Repeat("x", 500) + token + " trailing response")

	got := safeSnippet(body, token)

	for _, leaked := range []string{token, token[:12]} {
		if strings.Contains(got, leaked) {
			t.Fatalf("safeSnippet leaked token fragment %q: %q", leaked, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("safeSnippet missing redaction marker: %q", got)
	}
}

func TestSafeSnippetSanitizesControlCharacters(t *testing.T) {
	got := safeSnippet([]byte("  first\nsecond\rthird	fourth\x00  "), "")
	if got != "first second third fourth" {
		t.Fatalf("safeSnippet = %q", got)
	}
}

func TestSafeSnippetDoesNotReconstructTokenAfterControlSanitization(t *testing.T) {
	const token = "secret-token"
	got := safeSnippet([]byte("request rejected: secret\x00-token"), token)
	if strings.Contains(got, token) {
		t.Fatalf("safeSnippet reconstructed token after sanitization: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("safeSnippet missing redaction marker: %q", got)
	}
}

func TestSafeSnippetBoundsOutput(t *testing.T) {
	got := safeSnippet([]byte(strings.Repeat("x", 600)), "")
	if len(got) != 512 {
		t.Fatalf("safeSnippet length = %d", len(got))
	}
}
