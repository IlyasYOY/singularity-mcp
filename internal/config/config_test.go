package config

import (
	"strings"
	"testing"
	"time"
)

func TestParsePrecedence(t *testing.T) {
	env := map[string]string{
		"SINGULARITY_TOKEN":                      "env-token",
		"SINGULARITY_BASE_URL":                   "https://env.example",
		"SINGULARITY_TIMEOUT":                    "10s",
		"SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL": "false",
	}
	got, err := Parse([]string{
		"-token", "cli-token",
		"-base-url", "https://cli.example",
		"-timeout", "5s",
		"-require-write-approval=true",
	}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Token != "cli-token" {
		t.Fatalf("token = %q", got.Config.Token)
	}
	if got.Config.BaseURL != "https://cli.example" {
		t.Fatalf("base URL = %q", got.Config.BaseURL)
	}
	if got.Config.Timeout != 5*time.Second {
		t.Fatalf("timeout = %s", got.Config.Timeout)
	}
	if !got.Config.RequireWriteApproval {
		t.Fatal("RequireWriteApproval = false")
	}
}

func TestParseApprovalTimeoutDefault(t *testing.T) {
	got, err := Parse(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.ApprovalTimeout != 2*time.Minute {
		t.Fatalf("approval timeout = %s", got.Config.ApprovalTimeout)
	}
}

func TestParseApprovalTimeoutFromEnv(t *testing.T) {
	got, err := Parse(nil, func(key string) string {
		if key == "SINGULARITY_MCP_APPROVAL_TIMEOUT" {
			return "45s"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.ApprovalTimeout != 45*time.Second {
		t.Fatalf("approval timeout = %s", got.Config.ApprovalTimeout)
	}
}

func TestParseApprovalTimeoutCLIOverridesEnv(t *testing.T) {
	got, err := Parse([]string{"-approval-timeout", "15s"}, func(key string) string {
		if key == "SINGULARITY_MCP_APPROVAL_TIMEOUT" {
			return "45s"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.ApprovalTimeout != 15*time.Second {
		t.Fatalf("approval timeout = %s", got.Config.ApprovalTimeout)
	}
}

func TestParseApprovalTimeoutCLIOverridesMalformedEnv(t *testing.T) {
	got, err := Parse([]string{"--approval-timeout=15s"}, func(key string) string {
		if key == "SINGULARITY_MCP_APPROVAL_TIMEOUT" {
			return "nope"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.ApprovalTimeout != 15*time.Second {
		t.Fatalf("approval timeout = %s", got.Config.ApprovalTimeout)
	}
}

func TestParseApprovalTimeoutRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  string
		want string
	}{
		{name: "malformed environment", env: "nope", want: "SINGULARITY_MCP_APPROVAL_TIMEOUT"},
		{name: "zero environment", env: "0s", want: "approval timeout must be positive"},
		{name: "negative environment", env: "-1s", want: "approval timeout must be positive"},
		{name: "malformed CLI", args: []string{"-approval-timeout", "nope"}, want: "approval-timeout"},
		{name: "zero CLI", args: []string{"-approval-timeout", "0s"}, want: "approval timeout must be positive"},
		{name: "negative CLI", args: []string{"-approval-timeout", "-1s"}, want: "approval timeout must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.args, func(key string) string {
				if key == "SINGULARITY_MCP_APPROVAL_TIMEOUT" {
					return tt.env
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseApprovalTimeoutMalformedEnvIgnoredForHelpAndVersion(t *testing.T) {
	tests := []struct {
		flag        string
		wantHelp    bool
		wantVersion bool
	}{
		{flag: "-help", wantHelp: true},
		{flag: "--h", wantHelp: true},
		{flag: "-version", wantVersion: true},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			got, err := Parse([]string{tt.flag}, func(key string) string {
				if key == "SINGULARITY_MCP_APPROVAL_TIMEOUT" {
					return "nope"
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.HelpOnly != tt.wantHelp || got.VersionOnly != tt.wantVersion {
				t.Fatalf("result = %#v", got)
			}
		})
	}
}

func TestParseRequireWriteApprovalFromEnvFalse(t *testing.T) {
	got, err := Parse(nil, func(key string) string {
		if key == "SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL" {
			return "false"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.RequireWriteApproval {
		t.Fatal("RequireWriteApproval = true")
	}
}

func TestParseRequireWriteApprovalCLIOverridesEnv(t *testing.T) {
	got, err := Parse([]string{"-require-write-approval=true"}, func(key string) string {
		if key == "SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL" {
			return "false"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Config.RequireWriteApproval {
		t.Fatal("RequireWriteApproval = false")
	}
}

func TestParseDefaultsWithoutToken(t *testing.T) {
	got, err := Parse([]string{"-token", "tok"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.BaseURL != DefaultBaseURL {
		t.Fatalf("base URL = %q", got.Config.BaseURL)
	}
	if got.Config.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %s", got.Config.Timeout)
	}
	if !got.Config.RequireWriteApproval {
		t.Fatal("RequireWriteApproval = false")
	}

	got, err = Parse(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Token != "" {
		t.Fatalf("token = %q", got.Config.Token)
	}
	if !got.Config.RequireWriteApproval {
		t.Fatal("RequireWriteApproval = false")
	}
}

func TestParseVersionSkipsTokenRequirement(t *testing.T) {
	got, err := Parse([]string{"-version"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !got.VersionOnly {
		t.Fatal("VersionOnly = false")
	}
}

func TestParseVersionIgnoresBadEnvTimeout(t *testing.T) {
	got, err := Parse([]string{"-version"}, func(key string) string {
		if key == "SINGULARITY_TIMEOUT" {
			return "nope"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.VersionOnly {
		t.Fatal("VersionOnly = false")
	}
}

func TestParseVersionIgnoresBadApprovalEnv(t *testing.T) {
	got, err := Parse([]string{"-version"}, func(key string) string {
		if key == "SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL" {
			return "sometimes"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.VersionOnly {
		t.Fatal("VersionOnly = false")
	}
}

func TestParseHelpRequested(t *testing.T) {
	tests := [][]string{
		{"-help"},
		{"--help"},
		{"-h"},
	}
	for _, args := range tests {
		t.Run(args[0], func(t *testing.T) {
			got, err := Parse(args, func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			if !got.HelpOnly {
				t.Fatal("HelpOnly = false")
			}
		})
	}
}

func TestParseHelpIgnoresBadEnv(t *testing.T) {
	got, err := Parse([]string{"--help"}, func(key string) string {
		switch key {
		case "SINGULARITY_TIMEOUT":
			return "nope"
		case "SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL":
			return "sometimes"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.HelpOnly {
		t.Fatal("HelpOnly = false")
	}
}

func TestParseRejectsBadInputs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{name: "bad env timeout", env: map[string]string{"SINGULARITY_TIMEOUT": "nope"}},
		{name: "bad approval flag", env: map[string]string{"SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL": "sometimes"}},
		{name: "bad base url", args: []string{"-token", "tok", "-base-url", "api.example"}},
		{name: "zero timeout", args: []string{"-token", "tok", "-timeout", "0s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.args, func(key string) string { return tt.env[key] })
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseRuntimeLimits(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		got, err := Parse(nil, func(string) string { return "" })
		if err != nil {
			t.Fatal(err)
		}
		if got.Config.OperationTimeout != 2*time.Minute || got.Config.MaxPages != 100 || got.Config.MaxItems != 10000 || got.Config.MaxResponseBytes != 1048576 {
			t.Fatalf("limits = %#v", got.Config)
		}
	})
	t.Run("environment", func(t *testing.T) {
		env := map[string]string{"SINGULARITY_MCP_OPERATION_TIMEOUT": "45s", "SINGULARITY_MCP_MAX_PAGES": "7", "SINGULARITY_MCP_MAX_ITEMS": "88", "SINGULARITY_MCP_MAX_RESPONSE_BYTES": "999"}
		got, err := Parse(nil, func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		if got.Config.OperationTimeout != 45*time.Second || got.Config.MaxPages != 7 || got.Config.MaxItems != 88 || got.Config.MaxResponseBytes != 999 {
			t.Fatalf("limits = %#v", got.Config)
		}
	})
	t.Run("CLI overrides malformed environment", func(t *testing.T) {
		env := map[string]string{"SINGULARITY_MCP_OPERATION_TIMEOUT": "bad", "SINGULARITY_MCP_MAX_PAGES": "bad", "SINGULARITY_MCP_MAX_ITEMS": "bad", "SINGULARITY_MCP_MAX_RESPONSE_BYTES": "bad"}
		got, err := Parse([]string{"--operation-timeout=3s", "--max-pages=2", "--max-items=3", "--max-response-bytes=4"}, func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		if got.Config.OperationTimeout != 3*time.Second || got.Config.MaxPages != 2 || got.Config.MaxItems != 3 || got.Config.MaxResponseBytes != 4 {
			t.Fatalf("limits = %#v", got.Config)
		}
	})
}

func TestParseRuntimeLimitsRejectInvalid(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"operation malformed", "SINGULARITY_MCP_OPERATION_TIMEOUT", "bad"}, {"operation zero", "SINGULARITY_MCP_OPERATION_TIMEOUT", "0s"},
		{"pages malformed", "SINGULARITY_MCP_MAX_PAGES", "bad"}, {"pages negative", "SINGULARITY_MCP_MAX_PAGES", "-1"},
		{"items zero", "SINGULARITY_MCP_MAX_ITEMS", "0"}, {"bytes negative", "SINGULARITY_MCP_MAX_RESPONSE_BYTES", "-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(nil, func(k string) string {
				if k == tc.key {
					return tc.value
				}
				return ""
			}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseRuntimeLimitsMalformedEnvBypassedForHelpAndVersion(t *testing.T) {
	for _, arg := range []string{"-help", "--help", "-h", "--h", "-version", "--version"} {
		t.Run(arg, func(t *testing.T) {
			got, err := Parse([]string{arg}, func(k string) string {
				if strings.HasPrefix(k, "SINGULARITY_MCP_") {
					return "bad"
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			if !got.HelpOnly && !got.VersionOnly {
				t.Fatalf("result=%#v", got)
			}
		})
	}
}

func TestParseTransportDefaultsAndPrecedence(t *testing.T) {
	got, err := Parse(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Transport != DefaultTransport || got.Config.HTTPAddress != DefaultHTTPAddress || got.Config.HTTPPath != DefaultHTTPPath {
		t.Fatalf("transport defaults = %#v", got.Config)
	}

	env := map[string]string{
		"SINGULARITY_MCP_TRANSPORT":    "http",
		"SINGULARITY_MCP_HTTP_ADDRESS": "127.0.0.1:9000",
		"SINGULARITY_MCP_HTTP_PATH":    "/env-mcp",
	}
	got, err = Parse([]string{"-http-address", "localhost:9100", "-http-path", "/cli-mcp"}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Transport != "http" || got.Config.HTTPAddress != "localhost:9100" || got.Config.HTTPPath != "/cli-mcp" {
		t.Fatalf("transport precedence = %#v", got.Config)
	}
}

func TestParseHTTPTransportSecurityValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{name: "reject static token", args: []string{"-transport", "http", "-token", "secret"}, want: "not allowed"},
		{name: "reject environment token", args: []string{"-transport", "http"}, env: map[string]string{"SINGULARITY_TOKEN": "secret"}, want: "not allowed"},
		{name: "reject public cleartext", args: []string{"-transport", "http", "-http-address", ":8080"}, want: "loopback"},
		{name: "reject partial TLS", args: []string{"-transport", "http", "-tls-cert", "cert.pem"}, want: "both TLS"},
		{name: "reject relative path", args: []string{"-transport", "http", "-http-path", "mcp"}, want: "clean absolute"},
		{name: "reject health conflict", args: []string{"-transport", "http", "-http-path", "/healthz"}, want: "healthz"},
		{name: "reject insecure upstream", args: []string{"-transport", "http", "-base-url", "http://api.example"}, want: "HTTPS Singularity"},
		{name: "reject unknown transport", args: []string{"-transport", "sse"}, want: "stdio or http"},
		{name: "reject TLS in stdio", args: []string{"-tls-cert", "cert.pem", "-tls-key", "key.pem"}, want: "require HTTP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.args, func(key string) string { return tt.env[key] })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseHTTPTransportAllowsSafeTopologies(t *testing.T) {
	for _, args := range [][]string{
		{"-transport", "http"},
		{"-transport", "HTTP", "-http-address", "[::1]:8080", "-base-url", "http://127.0.0.1:9090"},
		{"-transport", "http", "-http-address", ":443", "-tls-cert", "cert.pem", "-tls-key", "key.pem"},
	} {
		if _, err := Parse(args, func(string) string { return "" }); err != nil {
			t.Fatalf("Parse(%v): %v", args, err)
		}
	}
}
