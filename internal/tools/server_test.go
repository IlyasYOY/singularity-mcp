package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IlyasYOY/singularity-mcp/internal/singularity"
	"github.com/IlyasYOY/singularity-mcp/openapi"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestToolContractOperationVariants(t *testing.T) {
	catalog := testCatalog(t)
	for _, group := range catalog.Groups {
		tool := toolForGroup(group)
		for _, op := range group.Operations {
			if !strings.Contains(tool.Description, op.Name) {
				t.Fatalf("%s description missing operation %s: %q", group.ToolName, op.Name, tool.Description)
			}
			label := "Read:"
			if operationRequiresApproval(op) {
				label = "Write:"
			}
			if !strings.Contains(tool.Description, label) {
				t.Fatalf("%s description missing %s: %q", group.ToolName, label, tool.Description)
			}
		}
		raw := tool.RawInputSchema
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		variants, ok := schema["oneOf"].([]any)
		if !ok || len(variants) != len(group.Operations) {
			t.Fatalf("%s variants=%d want=%d schema=%s", group.ToolName, len(variants), len(group.Operations), raw)
		}
		if strings.Contains(string(raw), `"$ref"`) {
			t.Fatalf("%s contains ref: %s", group.ToolName, raw)
		}
		for _, op := range group.Operations {
			variant := operationVariant(t, variants, op.Name)
			if variant["additionalProperties"] != false {
				t.Fatalf("%s.%s is not strict", group.ToolName, op.Name)
			}
			props := variant["properties"].(map[string]any)
			if op.Name == "create" {
				body := props["body"].(map[string]any)
				if body["additionalProperties"] != false {
					t.Fatalf("%s create body is not strict", group.ToolName)
				}
			}
			if op.Name == "search" {
				for key, want := range map[string]any{"all": true, "compact": true, "limit": float64(20)} {
					if props[key].(map[string]any)["default"] != want {
						t.Fatalf("%s search %s default=%v", group.ToolName, key, props[key].(map[string]any)["default"])
					}
				}
				limit := props["limit"].(map[string]any)
				if limit["type"] != "integer" || limit["minimum"] != float64(1) || limit["maximum"] != float64(100) {
					t.Fatalf("limit=%v", limit)
				}
				fields := props["fields"].(map[string]any)["items"].(map[string]any)["enum"].([]any)
				hasNote := false
				for _, field := range fields {
					hasNote = hasNote || field == "note"
				}
				if hasNote {
					t.Fatalf("%s search fields must not advertise opaque note IDs: %v", group.ToolName, fields)
				}
				for _, key := range []string{"tag", "tags", "tagMode", "checked", "priority"} {
					_, exists := props[key]
					if want := op.Tag == "task"; exists != want {
						t.Fatalf("%s search field %s exists=%v want=%v", group.ToolName, key, exists, want)
					}
				}
				if _, exists := props["parent"]; !exists {
					t.Fatalf("%s search must expose parent", group.ToolName)
				}
				if _, exists := props["isNotebook"]; exists != (op.Tag == "project") {
					t.Fatalf("%s search isNotebook exists=%v", group.ToolName, exists)
				}
				if op.Tag == "task" {
					for _, key := range []string{"checked", "priority"} {
						values := props[key].(map[string]any)["enum"].([]any)
						if fmt.Sprint(values) != "[0 1 2]" {
							t.Fatalf("%s search %s enum=%v", group.ToolName, key, values)
						}
					}
				}
			}
			for _, key := range []string{"maxCount", "offset"} {
				if field, ok := props[key].(map[string]any); ok && field["type"] != "integer" {
					t.Fatalf("%s.%s %s type=%v", group.ToolName, op.Name, key, field["type"])
				}
			}
			if op.Tag == "task" && (op.Name == "list" || op.Name == "search") {
				for _, key := range []string{"startDateFrom", "startDateTo"} {
					if props[key].(map[string]any)["format"] != "date-time" {
						t.Fatalf("%s.%s %s format=%v", group.ToolName, op.Name, key, props[key])
					}
				}
			}
			if group.ToolName == "singularity_tasks" && (op.Name == "inbox" || op.Name == "overdue" || op.Name == "today" || op.Name == "only-today") {
				if _, exists := props["all"]; exists {
					t.Fatalf("%s must not advertise ignored all control: %v", op.Name, props)
				}
				_, hasCompact := props["compact"]
				wantCompact := op.Name != "inbox"
				if hasCompact != wantCompact {
					t.Fatalf("%s compact exists=%v want=%v: %v", op.Name, hasCompact, wantCompact, props)
				}
				wantFields := 1
				if wantCompact {
					wantFields++
				}
				if len(props) != wantFields {
					t.Fatalf("%s synthetic fields=%v", op.Name, props)
				}
			}
			if op.Name == "delete" && props["confirm"].(map[string]any)["const"] != true {
				t.Fatalf("delete confirm=%v", props["confirm"])
			}
			if op.Name == "delete_bulk" && props["confirm"].(map[string]any)["const"] != "DELETE" {
				t.Fatalf("bulk confirm=%v", props["confirm"])
			}
		}
	}
}

func operationVariant(t *testing.T, variants []any, name string) map[string]any {
	t.Helper()
	for _, raw := range variants {
		variant := raw.(map[string]any)
		props := variant["properties"].(map[string]any)
		if props["operation"].(map[string]any)["const"] == name {
			return variant
		}
	}
	t.Fatalf("variant %s missing", name)
	return nil
}

func TestInputSchemaValidationRejectsInvalidCallsBeforeHTTP(t *testing.T) {
	catalog := testCatalog(t)
	var calls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(`{"ok":true}`)) }))
	defer api.Close()
	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: false})
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	startClient(t, c)
	bad := []struct {
		tool string
		args map[string]any
	}{
		{"singularity_projects", map[string]any{"operation": "create"}},
		{"singularity_projects", map[string]any{"operation": "create", "body": map[string]any{}}},
		{"singularity_projects", map[string]any{"operation": "create", "body": map[string]any{"title": "x", "isNotebook": "yes"}}},
		{"singularity_projects", map[string]any{"operation": "list", "unknown": true}},
		{"singularity_projects", map[string]any{"operation": "search", "query": "x", "limit": 1.5}},
		{"singularity_projects", map[string]any{"operation": "delete", "id": "P-1", "confirm": false}},
		{"singularity_tasks", map[string]any{"operation": "inbox", "all": false}},
		{"singularity_tasks", map[string]any{"operation": "inbox", "compact": false}},
		{"singularity_tasks", map[string]any{"operation": "overdue", "all": false}},
		{"singularity_tasks", map[string]any{"operation": "list", "startDateFrom": "2026-07-04"}},
		{"singularity_tasks", map[string]any{"operation": "search", "query": "x", "checked": 3}},
	}
	for _, tt := range bad {
		req := mcp.CallToolRequest{}
		req.Params.Name = tt.tool
		req.Params.Arguments = tt.args
		result, err := c.CallTool(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatalf("accepted invalid args: %v", tt.args)
		}
	}
	if calls != 0 {
		t.Fatalf("http calls=%d", calls)
	}
}

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
		if tool.Name == "singularity_tasks" || tool.Name == "singularity_projects" || tool.Name == "singularity_time_stats" {
			// Detailed raw schemas are covered by TestToolContractOperationVariants;
			// mcp-go's typed client representation does not preserve root oneOf.
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
	if !strings.Contains(text, `"swaggerTotal":51`) || !strings.Contains(text, `"swaggerExposed":41`) || !strings.Contains(text, `"synthetic":7`) || !strings.Contains(text, `"omitted":10`) || !strings.Contains(text, `"exposedTotal":48`) || !strings.Contains(text, "kanban-status") {
		t.Fatalf("capabilities = %s", text)
	}
	if !strings.Contains(text, `"requireWriteApproval":true`) {
		t.Fatalf("capabilities missing write approval policy: %s", text)
	}
}

func TestStructuredToolResultMatchesText(t *testing.T) {
	catalog := testCatalog(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"projects":[]}`)) }))
	defer api.Close()
	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: false})
	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	startClient(t, c)
	req := mcp.CallToolRequest{}
	req.Params.Name = "singularity_projects"
	req.Params.Arguments = map[string]any{"operation": "list"}
	result, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.StructuredContent == nil {
		t.Fatal("structured content is nil")
	}
	var text any
	if err := json.Unmarshal([]byte(resultText(result)), &text); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.StructuredContent, text) {
		t.Fatalf("structured=%v text=%v", result.StructuredContent, text)
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

	if got := call(map[string]any{}); !strings.Contains(got, "operation") {
		t.Fatalf("missing operation error: %s", got)
	}
	if got := call(map[string]any{"operation": "nope"}); !strings.Contains(got, "validation") {
		t.Fatalf("invalid operation error: %s", got)
	}
	if got := call(map[string]any{"operation": "get"}); !strings.Contains(got, "Missing:[id]") {
		t.Fatalf("missing id error: %s", got)
	}
	got := call(map[string]any{"operation": "list"})
	if !strings.Contains(got, `"status":403`) || strings.Contains(got, "secret-token") {
		t.Fatalf("api error = %s", got)
	}
}

func TestApprovalUsesPreparedPayloadAndInvalidWritesSkipApproval(t *testing.T) {
	catalog := testCatalog(t)
	var gotBody map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"id":"P-1"}`))
	}))
	defer api.Close()
	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true, ApprovalTimeout: time.Second})
	h := &testElicitationHandler{action: mcp.ElicitationResponseActionAccept, approved: true}
	c := newInProcessClientWithElicitation(t, srv, h)
	defer c.Close()
	startClient(t, c)
	call := func(body map[string]any) *mcp.CallToolResult {
		req := mcp.CallToolRequest{}
		req.Params.Name = "singularity_projects"
		req.Params.Arguments = map[string]any{"operation": "create", "body": body}
		result, err := c.CallTool(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	missingTitle := call(map[string]any{})
	if !missingTitle.IsError || h.calls != 0 {
		t.Fatalf("missing-title result=%v approvals=%d", missingTitle, h.calls)
	}
	deleteReq := mcp.CallToolRequest{}
	deleteReq.Params.Name = "singularity_projects"
	deleteReq.Params.Arguments = map[string]any{"operation": "delete", "id": "P-1"}
	missingConfirm, err := c.CallTool(context.Background(), deleteReq)
	if err != nil {
		t.Fatal(err)
	}
	if !missingConfirm.IsError || h.calls != 0 {
		t.Fatalf("missing-confirm result=%v approvals=%d", missingConfirm, h.calls)
	}
	invalid := call(map[string]any{"title": "x", "note": "a", "noteText": "b"})
	if !invalid.IsError || h.calls != 0 {
		t.Fatalf("invalid result=%v approvals=%d", invalid, h.calls)
	}
	valid := call(map[string]any{"title": "x", "noteText": "plain"})
	if valid.IsError {
		t.Fatalf("valid error=%s", resultText(valid))
	}
	if h.calls != 1 || !strings.Contains(h.last.Params.Message, `"note":"plain"`) || strings.Contains(h.last.Params.Message, "noteText") {
		t.Fatalf("approval=%q", h.last.Params.Message)
	}
	if gotBody["note"] != "plain" {
		t.Fatalf("body=%v", gotBody)
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

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true, ApprovalTimeout: time.Second})
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

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true, ApprovalTimeout: time.Second})
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

func TestWriteApprovalTimeoutFailsClosedBeforeAPICall(t *testing.T) {
	catalog := testCatalog(t)
	var httpCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer api.Close()

	const approvalTimeout = 25 * time.Millisecond
	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{
		RequireWriteApproval: true,
		ApprovalTimeout:      approvalTimeout,
	})
	handler := &blockingElicitationHandler{cancelled: make(chan struct{})}
	c := newInProcessClientWithElicitation(t, srv, handler)
	defer c.Close()
	startClient(t, c)

	req := mcp.CallToolRequest{}
	req.Params.Name = "singularity_projects"
	req.Params.Arguments = map[string]any{"operation": "delete", "id": "id-1", "confirm": true}
	outerCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	result, err := c.CallTool(outerCtx, req)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed >= time.Second {
		t.Fatalf("call returned after outer deadline: %s", elapsed)
	}
	if !result.IsError || resultText(result) != "write operation blocked: approval request timed out" {
		t.Fatalf("timeout result = %#v (%q)", result, resultText(result))
	}
	select {
	case <-handler.cancelled:
	case <-time.After(time.Second):
		t.Fatal("elicitation context was not cancelled")
	}
	if httpCalls != 0 {
		t.Fatalf("http calls = %d", httpCalls)
	}
}

func TestWriteApprovalTimeoutRejectsLateSuccessfulApproval(t *testing.T) {
	catalog := testCatalog(t)
	var httpCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer api.Close()

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{
		RequireWriteApproval: true,
		ApprovalTimeout:      25 * time.Millisecond,
	})
	handler := &lateApprovalHandler{cancelled: make(chan struct{})}
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
	if !result.IsError || resultText(result) != "write operation blocked: approval request timed out" {
		t.Fatalf("late approval result = %#v (%q)", result, resultText(result))
	}
	select {
	case <-handler.cancelled:
	case <-time.After(time.Second):
		t.Fatal("elicitation context was not cancelled")
	}
	if got := httpCalls.Load(); got != 0 {
		t.Fatalf("http calls = %d", got)
	}
}

func TestWriteApprovalTimeoutBoundsHandlerIgnoringCancellation(t *testing.T) {
	catalog := testCatalog(t)
	var httpCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer api.Close()

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{
		RequireWriteApproval: true,
		ApprovalTimeout:      25 * time.Millisecond,
	})
	handler := &ignoringCancellationHandler{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	c := newInProcessClientWithElicitation(t, srv, handler)
	defer c.Close()
	startClient(t, c)

	req := mcp.CallToolRequest{}
	req.Params.Name = "singularity_projects"
	req.Params.Arguments = map[string]any{"operation": "delete", "id": "id-1", "confirm": true}
	type callResult struct {
		result *mcp.CallToolResult
		err    error
	}
	returned := make(chan callResult, 1)
	go func() {
		result, err := c.CallTool(context.Background(), req)
		returned <- callResult{result: result, err: err}
	}()

	<-handler.started
	select {
	case got := <-returned:
		close(handler.release)
		<-handler.done
		if got.err != nil {
			t.Fatal(got.err)
		}
		if !got.result.IsError || resultText(got.result) != "write operation blocked: approval request timed out" {
			t.Fatalf("blocked handler result = %#v (%q)", got.result, resultText(got.result))
		}
	case <-time.After(250 * time.Millisecond):
		close(handler.release)
		<-handler.done
		<-returned
		t.Fatal("tool call remained blocked after approval timeout")
	}
	if got := httpCalls.Load(); got != 0 {
		t.Fatalf("http calls = %d", got)
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

			srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true, ApprovalTimeout: time.Second})
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

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true, ApprovalTimeout: time.Second})
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

	srv := NewServerWithOptions(testClient(t, api.URL), catalog, "test", Options{RequireWriteApproval: true, ApprovalTimeout: time.Second})
	handler := &testElicitationHandler{action: mcp.ElicitationResponseActionAccept, approved: true}
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
	if handler.calls != 1 {
		t.Fatalf("elicitation calls = %d", handler.calls)
	}
	message := handler.last.Params.Message
	for _, want := range []string{"tool=singularity_projects", "operation=create", "method=POST", "path=/v2/project", "body="} {
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

type blockingElicitationHandler struct {
	cancelled chan struct{}
}

func (h *blockingElicitationHandler) Elicit(ctx context.Context, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	_ = request
	<-ctx.Done()
	close(h.cancelled)
	return nil, ctx.Err()
}

type lateApprovalHandler struct {
	cancelled chan struct{}
}

func (h *lateApprovalHandler) Elicit(ctx context.Context, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	_ = request
	<-ctx.Done()
	close(h.cancelled)
	return &mcp.ElicitationResult{ElicitationResponse: mcp.ElicitationResponse{
		Action:  mcp.ElicitationResponseActionAccept,
		Content: map[string]any{"approved": true},
	}}, nil
}

type ignoringCancellationHandler struct {
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (h *ignoringCancellationHandler) Elicit(ctx context.Context, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	_ = ctx
	_ = request
	close(h.started)
	<-h.release
	close(h.done)
	return &mcp.ElicitationResult{ElicitationResponse: mcp.ElicitationResponse{
		Action:  mcp.ElicitationResponseActionAccept,
		Content: map[string]any{"approved": true},
	}}, nil
}

func newInProcessClientWithElicitation(t *testing.T, srv *server.MCPServer, handler interface {
	Elicit(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error)
}) *client.Client {
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
