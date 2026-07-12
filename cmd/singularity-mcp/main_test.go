package main

import (
	"strings"
	"testing"
	"time"

	"github.com/IlyasYOY/singularity-mcp/internal/config"
)

func TestUsageIncludesFlagsAndDefaults(t *testing.T) {
	got := usage("test-version")
	for _, want := range []string{
		"singularity-mcp test-version",
		"Usage:",
		"-token string",
		"SINGULARITY_TOKEN",
		"-base-url string",
		"https://api.singularity-app.com",
		"-timeout duration",
		"30s",
		"-approval-timeout duration",
		"SINGULARITY_MCP_APPROVAL_TIMEOUT",
		"2m0s",
		"-operation-timeout duration",
		"SINGULARITY_MCP_OPERATION_TIMEOUT",
		"-max-pages int",
		"SINGULARITY_MCP_MAX_PAGES",
		"-max-items int",
		"SINGULARITY_MCP_MAX_ITEMS",
		"-max-response-bytes int",
		"SINGULARITY_MCP_MAX_RESPONSE_BYTES",
		"-require-write-approval",
		"-version",
		"-help, -h",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage() missing %q in:\n%s", want, got)
		}
	}
}

func TestToolOptionsIncludesApprovalTimeout(t *testing.T) {
	cfg := config.Config{
		RequireWriteApproval: true,
		ApprovalTimeout:      17 * time.Second,
		OperationTimeout:     23 * time.Second,
	}

	got := toolOptions(cfg)

	if !got.RequireWriteApproval {
		t.Fatal("RequireWriteApproval = false")
	}
	if got.ApprovalTimeout != 17*time.Second {
		t.Fatalf("ApprovalTimeout = %s", got.ApprovalTimeout)
	}
	if got.OperationTimeout != 23*time.Second {
		t.Fatalf("OperationTimeout = %s", got.OperationTimeout)
	}
}
