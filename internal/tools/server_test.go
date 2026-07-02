package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IlyasYOY/singularity-mcp/internal/singularity"
	"github.com/IlyasYOY/singularity-mcp/openapi"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestToolSchemasAndResources(t *testing.T) {
	catalog := testCatalog(t)
	srv := NewServer(testClient(t, "http://127.0.0.1"), catalog, "test")
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	startClient(t, c)

	tools, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 8 {
		t.Fatalf("tools = %d", len(tools.Tools))
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
		if strings.Contains(tool.Name, "kanban") {
			t.Fatalf("kanban tool exposed: %s", tool.Name)
		}
		if tool.Name == "singularity_time_stats" {
			raw, _ := json.Marshal(tool.InputSchema)
			if !strings.Contains(string(raw), "delete_bulk") {
				t.Fatalf("time stats schema missing delete_bulk: %s", raw)
			}
		}
		if tool.Name == "singularity_tasks" {
			raw, _ := json.Marshal(tool.InputSchema)
			schema := string(raw)
			if !strings.Contains(schema, "inbox") || !strings.Contains(schema, "compact") {
				t.Fatalf("task schema missing inbox/compact: %s", raw)
			}
			if !strings.Contains(schema, "noteText") || !strings.Contains(schema, "Do not pass JSON or Quill Delta") {
				t.Fatalf("task schema missing plain note guidance: %s", raw)
			}
		}
		if tool.Name == "singularity_projects" {
			raw, _ := json.Marshal(tool.InputSchema)
			schema := string(raw)
			if !strings.Contains(schema, "noteText") || !strings.Contains(schema, "Do not pass JSON or Quill Delta") {
				t.Fatalf("project schema missing plain note guidance: %s", raw)
			}
		}
	}
	if !names["singularity_projects"] || !names["singularity_tasks"] {
		t.Fatalf("expected tools missing: %v", names)
	}

	resources, err := c.ListResources(context.Background(), mcp.ListResourcesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Resources) != 2 {
		t.Fatalf("resources = %d", len(resources.Resources))
	}

	read, err := c.ReadResource(context.Background(), mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{URI: "singularity://capabilities"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := read.Contents[0].(mcp.TextResourceContents).Text
	if !strings.Contains(text, `"exposed":42`) || !strings.Contains(text, "kanban-status") {
		t.Fatalf("capabilities = %s", text)
	}
}

func TestToolCallEveryOperation(t *testing.T) {
	catalog := testCatalog(t)
	var seen []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if r.Method == "DELETE" && strings.Contains(r.URL.Path, "/id-") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		response := responseForPath(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(response))
	}))
	defer api.Close()

	srv := NewServer(testClient(t, api.URL), catalog, "test")
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	startClient(t, c)

	for _, group := range catalog.Groups {
		for _, op := range group.Operations {
			req := mcp.CallToolRequest{}
			req.Params.Name = group.ToolName
			req.Params.Arguments = argsFor(op)
			result, err := c.CallTool(context.Background(), req)
			if err != nil {
				t.Fatalf("%s.%s protocol error: %v", group.ToolName, op.Name, err)
			}
			if result.IsError {
				t.Fatalf("%s.%s tool error: %s", group.ToolName, op.Name, resultText(result))
			}
			if !json.Valid([]byte(resultText(result))) {
				t.Fatalf("%s.%s non-json result: %s", group.ToolName, op.Name, resultText(result))
			}
		}
	}
	if len(seen) != catalog.ExposedOperationCount() {
		t.Fatalf("seen HTTP calls = %d", len(seen))
	}
}

func TestToolValidationAndAPIErrors(t *testing.T) {
	catalog := testCatalog(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"token secret-token rejected"}`))
	}))
	defer api.Close()

	srv := NewServer(testClient(t, api.URL), catalog, "test")
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	startClient(t, c)

	call := func(args map[string]any) string {
		req := mcp.CallToolRequest{}
		req.Params.Name = "singularity_projects"
		req.Params.Arguments = args
		result, err := c.CallTool(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatal("expected tool error")
		}
		return resultText(result)
	}

	if got := call(map[string]any{}); !strings.Contains(got, "operation is required") {
		t.Fatalf("missing operation error: %s", got)
	}
	if got := call(map[string]any{"operation": "nope"}); !strings.Contains(got, "invalid operation") {
		t.Fatalf("invalid operation error: %s", got)
	}
	if got := call(map[string]any{"operation": "get"}); !strings.Contains(got, "id is required") {
		t.Fatalf("missing id error: %s", got)
	}
	got := call(map[string]any{"operation": "list"})
	if !strings.Contains(got, `"status":403`) || strings.Contains(got, "secret-token") {
		t.Fatalf("api error = %s", got)
	}
}

func testCatalog(t *testing.T) *singularity.Catalog {
	t.Helper()
	catalog, err := singularity.NewCatalog(openapi.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func testClient(t *testing.T, baseURL string) *singularity.APIClient {
	t.Helper()
	client, err := singularity.NewAPIClient(baseURL, "secret-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func startClient(t *testing.T, c *client.Client) {
	t.Helper()
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "test-client", Version: "1.0.0"}
	if _, err := c.Initialize(context.Background(), init); err != nil {
		t.Fatal(err)
	}
}

func argsFor(op *singularity.Operation) map[string]any {
	args := map[string]any{"operation": op.Name}
	switch op.Name {
	case "get":
		args["id"] = "id-1"
	case "update":
		args["id"] = "id-1"
		args["body"] = bodyFor(op)
	case "delete":
		args["id"] = "id-1"
		args["confirm"] = true
	case "create":
		args["body"] = bodyFor(op)
	case "delete_bulk":
		args["confirm"] = "DELETE"
		args[op.QueryParams[0].Name] = "2026-01-01"
	case "list":
		args["maxCount"] = float64(1)
	}
	return args
}

func bodyFor(op *singularity.Operation) map[string]any {
	body := map[string]any{}
	for _, name := range op.BodyRequired {
		body[name] = valueForField(name)
	}
	return body
}

func valueForField(name string) any {
	switch name {
	case "progress":
		return float64(1)
	case "secondsPassed":
		return float64(60)
	default:
		return name + "-value"
	}
}

func responseForPath(path string) string {
	switch {
	case path == "/v2/project":
		return `{"projects":[]}`
	case path == "/v2/task-group":
		return `{"taskGroups":[]}`
	case path == "/v2/task":
		return `{"tasks":[]}`
	case path == "/v2/habit":
		return `{"habits":[]}`
	case path == "/v2/habit-progress":
		return `{"progressRecords":[]}`
	case path == "/v2/checklist-item":
		return `{"checklistItems":[]}`
	case path == "/v2/tag":
		return `{"tags":[]}`
	case path == "/v2/time-stat":
		return `{"timeStats":[]}`
	default:
		return `{"id":"ok"}`
	}
}

func resultText(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}
