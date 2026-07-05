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
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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
			for _, want := range []string{"inbox", "overdue", "today", "only-today", "search", "compact", "query", "fields", "limit", "tag", "tags", "tagMode"} {
				if !strings.Contains(schema, want) {
					t.Fatalf("task schema missing %s: %s", want, raw)
				}
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
	if !strings.Contains(text, `"exposed":48`) || !strings.Contains(text, "kanban-status") {
		t.Fatalf("capabilities = %s", text)
	}
	if !strings.Contains(text, `"requireWriteApproval":true`) {
		t.Fatalf("capabilities missing write approval policy: %s", text)
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

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: false})
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

func TestRequireWriteApprovalAllowsSearchWithoutElicitation(t *testing.T) {
	catalog := testCatalog(t)
	var httpCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tasks":[{"id":"T-1","title":"MCP search"}]}`))
	}))
	defer api.Close()

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true})
	handler := &testElicitationHandler{action: mcp.ElicitationResponseActionAccept, approved: true}
	c := newInProcessClientWithElicitation(t, srv, handler)
	defer c.Close()
	startClient(t, c)

	req := mcp.CallToolRequest{}
	req.Params.Name = "singularity_tasks"
	req.Params.Arguments = map[string]any{"operation": "search", "query": "mcp", "all": false}
	result, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", resultText(result))
	}
	if handler.calls != 0 {
		t.Fatalf("elicitation calls = %d", handler.calls)
	}
	if httpCalls != 1 {
		t.Fatalf("http calls = %d", httpCalls)
	}
	if !strings.Contains(resultText(result), `"count":1`) {
		t.Fatalf("result = %s", resultText(result))
	}
}

func TestRequireWriteApprovalAllowsReadsWithoutElicitation(t *testing.T) {
	catalog := testCatalog(t)
	var httpCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"projects":[]}`))
	}))
	defer api.Close()

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true})
	handler := &testElicitationHandler{action: mcp.ElicitationResponseActionAccept, approved: true}
	c := newInProcessClientWithElicitation(t, srv, handler)
	defer c.Close()
	startClient(t, c)

	req := mcp.CallToolRequest{}
	req.Params.Name = "singularity_projects"
	req.Params.Arguments = map[string]any{"operation": "list"}
	result, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", resultText(result))
	}
	if handler.calls != 0 {
		t.Fatalf("elicitation calls = %d", handler.calls)
	}
	if httpCalls != 1 {
		t.Fatalf("http calls = %d", httpCalls)
	}
}

func TestRequireWriteApprovalBlocksUnapprovedWritesBeforeAPICall(t *testing.T) {
	tests := []struct {
		name     string
		action   mcp.ElicitationResponseAction
		approved bool
	}{
		{name: "decline", action: mcp.ElicitationResponseActionDecline},
		{name: "cancel", action: mcp.ElicitationResponseActionCancel},
		{name: "approved false", action: mcp.ElicitationResponseActionAccept, approved: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := testCatalog(t)
			var httpCalls int
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				httpCalls++
				w.WriteHeader(http.StatusNoContent)
			}))
			defer api.Close()

			srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true})
			handler := &testElicitationHandler{action: tt.action, approved: tt.approved}
			c := newInProcessClientWithElicitation(t, srv, handler)
			defer c.Close()
			startClient(t, c)

			req := mcp.CallToolRequest{}
			req.Params.Name = "singularity_projects"
			req.Params.Arguments = map[string]any{"operation": "delete", "id": "id-1", "confirm": true}
			result, err := c.CallTool(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || !strings.Contains(resultText(result), "write operation blocked") {
				t.Fatalf("expected approval error, got: %s", resultText(result))
			}
			if handler.calls != 1 {
				t.Fatalf("elicitation calls = %d", handler.calls)
			}
			message := handler.last.Params.Message
			for _, want := range []string{"tool=singularity_projects", "operation=delete", "method=DELETE", "path=/v2/project/{id}", "id=id-1"} {
				if !strings.Contains(message, want) {
					t.Fatalf("approval message missing %q: %q", want, message)
				}
			}
			if httpCalls != 0 {
				t.Fatalf("http calls = %d", httpCalls)
			}
		})
	}
}

func TestRequireWriteApprovalFailsClosedWithoutClientElicitationSupport(t *testing.T) {
	catalog := testCatalog(t)
	var httpCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer api.Close()

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true})
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	startClient(t, c)

	req := mcp.CallToolRequest{}
	req.Params.Name = "singularity_projects"
	req.Params.Arguments = map[string]any{"operation": "delete", "id": "id-1", "confirm": true}
	result, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(resultText(result), "write operation blocked") || !strings.Contains(resultText(result), "elicitation") {
		t.Fatalf("expected elicitation support error, got: %s", resultText(result))
	}
	if httpCalls != 0 {
		t.Fatalf("http calls = %d", httpCalls)
	}
}

func TestRequireWriteApprovalAllowsApprovedWrites(t *testing.T) {
	catalog := testCatalog(t)
	var httpCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"created"}`))
	}))
	defer api.Close()

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true})
	handler := &testElicitationHandler{action: mcp.ElicitationResponseActionAccept, approved: true}
	c := newInProcessClientWithElicitation(t, srv, handler)
	defer c.Close()
	startClient(t, c)

	req := mcp.CallToolRequest{}
	req.Params.Name = "singularity_projects"
	req.Params.Arguments = map[string]any{"operation": "create", "maxCount": float64(1), "body": map[string]any{"title": "new"}}
	result, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", resultText(result))
	}
	if handler.calls != 1 {
		t.Fatalf("elicitation calls = %d", handler.calls)
	}
	message := handler.last.Params.Message
	for _, want := range []string{"tool=singularity_projects", "operation=create", "method=POST", "path=/v2/project", "args=", "maxCount", "body="} {
		if !strings.Contains(message, want) {
			t.Fatalf("approval message missing %q: %q", want, message)
		}
	}
	if httpCalls != 1 {
		t.Fatalf("http calls = %d", httpCalls)
	}
}

func TestRequireWriteApprovalDisabledAllowsWritesWithoutElicitation(t *testing.T) {
	catalog := testCatalog(t)
	var httpCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"created"}`))
	}))
	defer api.Close()

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: false})
	handler := &testElicitationHandler{action: mcp.ElicitationResponseActionDecline}
	c := newInProcessClientWithElicitation(t, srv, handler)
	defer c.Close()
	startClient(t, c)

	req := mcp.CallToolRequest{}
	req.Params.Name = "singularity_projects"
	req.Params.Arguments = map[string]any{"operation": "create", "body": map[string]any{"title": "new"}}
	result, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", resultText(result))
	}
	if handler.calls != 0 {
		t.Fatalf("elicitation calls = %d", handler.calls)
	}
	if httpCalls != 1 {
		t.Fatalf("http calls = %d", httpCalls)
	}
}

func TestOperationRequiresApprovalByHTTPMethod(t *testing.T) {
	if operationRequiresApproval(&singularity.Operation{Method: http.MethodGet}) {
		t.Fatal("GET operation requires approval")
	}
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		if !operationRequiresApproval(&singularity.Operation{Method: method}) {
			t.Fatalf("%s operation does not require approval", method)
		}
	}
}

func TestApprovalMessageBoundsBodyPreview(t *testing.T) {
	longValue := strings.Repeat("x", approvalPreviewLimit*2)
	message := approvalMessage("singularity_projects", &singularity.Operation{
		Name:   "create",
		Method: http.MethodPost,
		Path:   "/v2/project",
	}, map[string]any{
		"operation": "create",
		"body":      map[string]any{"title": longValue},
	})
	if !strings.Contains(message, "body=") || !strings.Contains(message, "…") {
		t.Fatalf("message missing bounded body preview: %q", message)
	}
	if strings.Contains(message, longValue) {
		t.Fatal("message contains unbounded body value")
	}
	if len(message) >= 2000 {
		t.Fatalf("message too long: %d", len(message))
	}
}

type testElicitationHandler struct {
	action   mcp.ElicitationResponseAction
	approved bool
	calls    int
	last     mcp.ElicitationRequest
}

func (h *testElicitationHandler) Elicit(ctx context.Context, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	_ = ctx
	h.calls++
	h.last = request
	return &mcp.ElicitationResult{ElicitationResponse: mcp.ElicitationResponse{
		Action:  h.action,
		Content: map[string]any{"approved": h.approved},
	}}, nil
}

func newInProcessClientWithElicitation(t *testing.T, srv *server.MCPServer, handler *testElicitationHandler) *client.Client {
	t.Helper()
	return client.NewClient(
		transport.NewInProcessTransportWithOptions(srv, transport.WithElicitationHandler(handler)),
		client.WithElicitationHandler(handler),
	)
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
	case "search":
		args["query"] = "x"
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
