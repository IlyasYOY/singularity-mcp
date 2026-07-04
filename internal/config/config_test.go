package config

import (
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
