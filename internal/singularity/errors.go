package singularity

import (
	"fmt"
	"strings"
	"unicode"
)

type ValidationError struct {
	Message string
}

func NewValidationError(message string) *ValidationError {
	return &ValidationError{Message: message}
}

func (e *ValidationError) Error() string {
	return e.Message
}

type APIError struct {
	Status   int
	Method   string
	Path     string
	Response string
}

func NewAPIError(status int, method, path string, body []byte, token string) *APIError {
	return &APIError{
		Status:   status,
		Method:   method,
		Path:     path,
		Response: safeSnippet(body, token),
	}
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Singularity API error: status=%d method=%s path=%s response=%q", e.Status, e.Method, e.Path, e.Response)
}

func safeSnippet(body []byte, token string) string {
	const limit = 512
	text := string(body)
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '	' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	if token != "" {
		text = strings.ReplaceAll(text, token, "[REDACTED]")
	}
	text = strings.TrimSpace(text)
	if len(text) > limit {
		text = text[:limit]
	}
	return text
}
