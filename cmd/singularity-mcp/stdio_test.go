package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestBinaryHelp(t *testing.T) {
	tests := []string{"-help", "--help", "-h"}
	for _, flag := range tests {
		t.Run(flag, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, "go", "run", ".", flag)
			cmd.Env = append(cmd.Environ(),
				"SINGULARITY_TOKEN=",
				"SINGULARITY_TIMEOUT=nope",
				"SINGULARITY_MCP_APPROVAL_TIMEOUT=nope",
				"SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL=sometimes",
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				t.Fatalf("run: %v, stderr: %s", err, stderr.String())
			}

			got := stdout.String()
			for _, want := range []string{
				fmt.Sprintf("singularity-mcp %s", version),
				"Usage:",
				"-token string",
				"-base-url string",
				"-timeout duration",
				"-approval-timeout duration",
				"-require-write-approval",
				"-version",
				"-help, -h",
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("stdout missing %q in:\n%s", want, got)
				}
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestBinaryStdioSmoke(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-token" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v2/project" {
			t.Fatalf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"projects":[]}`))
	}))
	defer api.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "run", ".", "-token", "fake-token", "-base-url", api.URL)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	enc := json.NewEncoder(stdin)
	dec := json.NewDecoder(bufio.NewReader(stdout))

	send := func(value any) {
		t.Helper()
		if err := enc.Encode(value); err != nil {
			t.Fatalf("send: %v, stderr: %s", err, stderr.String())
		}
	}
	read := func(id int) map[string]any {
		t.Helper()
		for {
			var msg map[string]any
			if err := dec.Decode(&msg); err != nil {
				t.Fatalf("read id %d: %v, stderr: %s", id, err, stderr.String())
			}
			if got, ok := msg["id"].(float64); ok && int(got) == id {
				return msg
			}
		}
	}

	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcp.LATEST_PROTOCOL_VERSION,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "stdio-smoke", "version": "1.0.0"},
		},
	})
	if resp := read(1); resp["error"] != nil {
		t.Fatalf("initialize error: %#v", resp["error"])
	}
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	})

	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	resp := read(2)
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 8 {
		t.Fatalf("tools = %d, resp = %#v", len(tools), resp)
	}

	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "singularity_projects",
			"arguments": map[string]any{
				"operation": "list",
				"maxCount":  float64(1),
			},
		},
	})
	resp = read(3)
	if resp["error"] != nil {
		t.Fatalf("tools/call error: %#v", resp["error"])
	}
	result, _ = resp["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("tool error: %#v", result)
	}

	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v, stderr: %s", err, stderr.String())
	}
}

func TestBinaryStdioStartsWithoutToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "run", ".")
	cmd.Env = append(cmd.Environ(), "SINGULARITY_TOKEN=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	enc := json.NewEncoder(stdin)
	dec := json.NewDecoder(bufio.NewReader(stdout))

	send := func(value any) {
		t.Helper()
		if err := enc.Encode(value); err != nil {
			t.Fatalf("send: %v, stderr: %s", err, stderr.String())
		}
	}
	read := func(id int) map[string]any {
		t.Helper()
		for {
			var msg map[string]any
			if err := dec.Decode(&msg); err != nil {
				t.Fatalf("read id %d: %v, stderr: %s", id, err, stderr.String())
			}
			if got, ok := msg["id"].(float64); ok && int(got) == id {
				return msg
			}
		}
	}

	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcp.LATEST_PROTOCOL_VERSION,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "stdio-no-token", "version": "1.0.0"},
		},
	})
	if resp := read(1); resp["error"] != nil {
		t.Fatalf("initialize error: %#v", resp["error"])
	}
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	})
	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	resp := read(2)
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 8 {
		t.Fatalf("tools = %d, resp = %#v", len(tools), resp)
	}

	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v, stderr: %s", err, stderr.String())
	}
}
