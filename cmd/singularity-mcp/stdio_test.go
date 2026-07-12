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
	"reflect"
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
				"-transport string",
				"-http-address string",
				"-http-path string",
				"-tls-cert string",
				"-tls-key string",
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
	operationCount := 0
	for _, rawTool := range tools {
		tool := rawTool.(map[string]any)
		schema := tool["inputSchema"].(map[string]any)
		variants, _ := schema["oneOf"].([]any)
		operationCount += len(variants)
	}
	if operationCount != 48 {
		t.Fatalf("operations = %d", operationCount)
	}
	for _, rawTool := range tools {
		tool := rawTool.(map[string]any)
		schema := tool["inputSchema"].(map[string]any)
		raw, _ := json.Marshal(schema)
		if strings.Contains(string(raw), `"$ref"`) {
			t.Fatalf("tool %v has unresolved ref: %s", tool["name"], raw)
		}
		if variants, _ := schema["oneOf"].([]any); len(variants) == 0 {
			t.Fatalf("tool %v has no operation variants: %s", tool["name"], raw)
		}
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
	if !reflect.DeepEqual(result["structuredContent"], map[string]any{"projects": []any{}}) {
		t.Fatalf("structured content: %#v", result["structuredContent"])
	}

	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v, stderr: %s", err, stderr.String())
	}
}

func TestBinaryStdioBoundedSearchAndStableErrors(t *testing.T) {
	var projectCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/project":
			projectCalls++
			_, _ = w.Write([]byte(`{"projects":[{"id":"P-1","title":"match"},{"id":"P-2","title":"match"}]}`))
		case "/v2/tag":
			_, _ = w.Write([]byte(`{"tags":[]}` + strings.Repeat(" ", 300)))
		case "/v2/habit":
			<-r.Context().Done()
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
		}
	}))
	defer api.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".", "-token", "fake-token", "-base-url", api.URL,
		"-max-response-bytes", "256", "-operation-timeout", "30ms")
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
			t.Fatal(err)
		}
	}
	read := func(id int) map[string]any {
		t.Helper()
		for {
			var msg map[string]any
			if err := dec.Decode(&msg); err != nil {
				t.Fatalf("read: %v stderr=%s", err, stderr.String())
			}
			if got, ok := msg["id"].(float64); ok && int(got) == id {
				return msg
			}
		}
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": mcp.LATEST_PROTOCOL_VERSION, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "phase-c", "version": "1"},
	}})
	if response := read(1); response["error"] != nil {
		t.Fatalf("initialize=%v", response)
	}
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}})
	call := func(id int, name string, args map[string]any) map[string]any {
		t.Helper()
		send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		response := read(id)
		result, _ := response["result"].(map[string]any)
		return result
	}

	search := call(2, "singularity_projects", map[string]any{"operation": "search", "query": "match", "all": false, "limit": 1})
	if isError, _ := search["isError"].(bool); isError {
		t.Fatalf("search=%v", search)
	}
	structured := search["structuredContent"].(map[string]any)
	pagination := structured["pagination"].(map[string]any)
	if structured["count"] != float64(1) || pagination["morePagesPossible"] != true || projectCalls != 1 {
		t.Fatalf("bounded search=%v calls=%d", structured, projectCalls)
	}

	resultText := func(result map[string]any) string {
		contents, _ := result["content"].([]any)
		if len(contents) == 0 {
			return ""
		}
		content, _ := contents[0].(map[string]any)
		text, _ := content["text"].(string)
		return text
	}
	oversized := call(3, "singularity_tags", map[string]any{"operation": "list"})
	if oversized["isError"] != true || !strings.Contains(resultText(oversized), `"type":"response_too_large"`) {
		t.Fatalf("oversized=%v", oversized)
	}
	timedOut := call(4, "singularity_habits", map[string]any{"operation": "list"})
	if timedOut["isError"] != true || !strings.Contains(resultText(timedOut), `"type":"operation_timeout"`) {
		t.Fatalf("timeout=%v", timedOut)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v stderr=%s", err, stderr.String())
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
