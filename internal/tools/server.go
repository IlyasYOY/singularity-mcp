package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/IlyasYOY/singularity-mcp/internal/singularity"
	"github.com/IlyasYOY/singularity-mcp/openapi"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Builder struct {
	Client               *singularity.APIClient
	Catalog              *singularity.Catalog
	Server               *server.MCPServer
	RequireWriteApproval bool
	ApprovalTimeout      time.Duration
	OperationTimeout     time.Duration
	TokenProvider        TokenProvider
	// approvalRequestSlots bounds a transport or handler that ignores context
	// cancellation to one orphaned elicitation request at a time.
	approvalRequestSlots chan struct{}
}

// Options configures optional MCP server behavior.
type Options struct {
	RequireWriteApproval bool
	ApprovalTimeout      time.Duration
	OperationTimeout     time.Duration
	TokenProvider        TokenProvider
}

// TokenProvider returns the Singularity bearer token for the current request.
// HTTP transports use this to keep credentials request-scoped.
type TokenProvider func(context.Context) (string, error)

const (
	defaultApprovalTimeout  = 2 * time.Minute
	defaultOperationTimeout = 2 * time.Minute
)

func NewServer(client *singularity.APIClient, catalog *singularity.Catalog, version string) *server.MCPServer {
	return NewServerWithOptions(client, catalog, version, Options{RequireWriteApproval: true})
}

// NewServerWithOptions creates a Singularity MCP server with optional safeguards.
func NewServerWithOptions(client *singularity.APIClient, catalog *singularity.Catalog, version string, options Options) *server.MCPServer {
	serverOptions := []server.ServerOption{
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithRecovery(),
		server.WithInputSchemaValidation(),
	}
	if options.RequireWriteApproval {
		serverOptions = append(serverOptions, server.WithElicitation())
	}
	mcpServer := server.NewMCPServer(
		"singularity-mcp",
		version,
		serverOptions...,
	)
	builder := Builder{
		Client:               client,
		Catalog:              catalog,
		Server:               mcpServer,
		RequireWriteApproval: options.RequireWriteApproval,
		ApprovalTimeout:      approvalTimeoutOrDefault(options.ApprovalTimeout),
		OperationTimeout:     operationTimeoutOrDefault(options.OperationTimeout),
		TokenProvider:        options.TokenProvider,
		approvalRequestSlots: make(chan struct{}, 1),
	}
	builder.Register(mcpServer)
	return mcpServer
}

func (b Builder) Register(mcpServer *server.MCPServer) {
	for _, group := range b.Catalog.Groups {
		mcpServer.AddTool(toolForGroup(group), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return b.handleTool(ctx, group, req)
		})
	}
	mcpServer.AddResource(
		mcp.NewResource(
			"singularity://openapi",
			"Singularity OpenAPI",
			mcp.WithMIMEType("application/json"),
			mcp.WithResourceDescription("Checked-in Singularity v2 OpenAPI snapshot."),
		),
		b.readResource,
	)
	mcpServer.AddResource(
		mcp.NewResource(
			"singularity://capabilities",
			"Singularity MCP Capabilities",
			mcp.WithMIMEType("application/json"),
			mcp.WithResourceDescription("Exposed Singularity tools, operations, and omitted groups."),
		),
		b.readResource,
	)
}

func (b Builder) handleTool(ctx context.Context, group *singularity.ToolGroup, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	apiClient := b.Client
	if b.TokenProvider != nil {
		token, err := b.TokenProvider(ctx)
		if err != nil {
			return mcp.NewToolResultError(singularity.StructuredError(err)), nil
		}
		apiClient = b.Client.ForToken(token)
	}
	args := req.GetArguments()
	operationName, ok := args["operation"].(string)
	if !ok || operationName == "" {
		return mcp.NewToolResultError(singularity.StructuredError(singularity.NewValidationError("operation is required"))), nil
	}
	op, ok := b.Catalog.Operation(group.ToolName, operationName)
	if !ok {
		return mcp.NewToolResultError(singularity.StructuredError(singularity.NewValidationError("invalid operation: " + operationName))), nil
	}
	prepared, err := apiClient.PrepareCall(op, args)
	if err != nil {
		return mcp.NewToolResultError(singularity.StructuredError(err)), nil
	}
	if approvalResult, proceed := b.requireWriteApproval(ctx, group.ToolName, op, prepared.Args); !proceed {
		return approvalResult, nil
	}
	operationCtx, cancel := context.WithTimeout(ctx, operationTimeoutOrDefault(b.OperationTimeout))
	raw, err := apiClient.ExecutePrepared(operationCtx, prepared)
	operationErr := operationCtx.Err()
	cancel()
	if errors.Is(operationErr, context.DeadlineExceeded) {
		timeout := operationTimeoutOrDefault(b.OperationTimeout)
		err = &singularity.ClientError{Code: "operation_timeout", Message: "Singularity API operation timed out", Metadata: map[string]any{"timeoutMs": timeout.Milliseconds()}}
	}
	if err != nil {
		return mcp.NewToolResultError(singularity.StructuredError(err)), nil
	}
	result := mcp.NewToolResultText(string(raw))
	var structured any
	if err := json.Unmarshal(raw, &structured); err != nil {
		return mcp.NewToolResultError(singularity.StructuredError(fmt.Errorf("decode successful response: %w", err))), nil
	}
	if _, ok := structured.(map[string]any); !ok {
		structured = map[string]any{"result": structured}
	}
	result.StructuredContent = structured
	return result, nil
}

func (b Builder) requireWriteApproval(ctx context.Context, toolName string, op *singularity.Operation, args map[string]any) (*mcp.CallToolResult, bool) {
	if !b.RequireWriteApproval || !operationRequiresApproval(op) {
		return nil, true
	}

	mcpServer := b.Server
	if mcpServer == nil {
		mcpServer = server.ServerFromContext(ctx)
	}
	if mcpServer == nil {
		return mcp.NewToolResultError("write operation blocked: approval session unavailable"), false
	}
	if !clientSupportsElicitation(ctx) {
		return mcp.NewToolResultError("write operation blocked: client does not support elicitation"), false
	}

	approvalCtx, cancel := context.WithTimeout(ctx, approvalTimeoutOrDefault(b.ApprovalTimeout))
	result, err := b.requestElicitation(approvalCtx, mcpServer, mcp.ElicitationRequest{
		Params: mcp.ElicitationParams{
			Message: approvalMessage(toolName, op, args),
			RequestedSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"approved": map[string]any{
						"type":        "boolean",
						"description": "Approve this Singularity write operation.",
					},
				},
				"required": []string{"approved"},
			},
		},
	})
	approvalErr := approvalCtx.Err()
	cancel()
	if errors.Is(approvalErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return mcp.NewToolResultError("write operation blocked: approval request timed out"), false
	}
	if approvalErr != nil {
		return mcp.NewToolResultError("write operation blocked: approval request failed: " + approvalErr.Error()), false
	}
	if err != nil {
		return mcp.NewToolResultError("write operation blocked: approval request failed: " + err.Error()), false
	}
	if result == nil {
		return mcp.NewToolResultError("write operation blocked: approval request returned no result"), false
	}
	if result.Action != mcp.ElicitationResponseActionAccept || !approvalAccepted(result.Content) {
		return mcp.NewToolResultError("write operation blocked: user did not approve"), false
	}
	return nil, true
}

type elicitationOutcome struct {
	result *mcp.ElicitationResult
	err    error
}

func (b Builder) requestElicitation(ctx context.Context, mcpServer *server.MCPServer, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	if b.approvalRequestSlots != nil {
		select {
		case b.approvalRequestSlots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	outcomes := make(chan elicitationOutcome, 1)
	go func() {
		if b.approvalRequestSlots != nil {
			defer func() { <-b.approvalRequestSlots }()
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				outcomes <- elicitationOutcome{err: fmt.Errorf("elicitation panic: %v", recovered)}
			}
		}()
		result, err := mcpServer.RequestElicitation(ctx, request)
		outcomes <- elicitationOutcome{result: result, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case outcome := <-outcomes:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return outcome.result, outcome.err
	}
}

func approvalTimeoutOrDefault(value time.Duration) time.Duration {
	if value == 0 {
		return defaultApprovalTimeout
	}
	return value
}

func operationTimeoutOrDefault(value time.Duration) time.Duration {
	if value == 0 {
		return defaultOperationTimeout
	}
	return value
}

func clientSupportsElicitation(ctx context.Context) bool {
	session := server.ClientSessionFromContext(ctx)
	if session == nil {
		return false
	}
	if _, ok := session.(server.SessionWithElicitation); !ok {
		return false
	}
	withInfo, ok := session.(server.SessionWithClientInfo)
	if !ok {
		return false
	}
	capabilities := withInfo.GetClientCapabilities()
	return capabilities.Elicitation != nil
}

func approvalAccepted(content any) bool {
	fields, ok := content.(map[string]any)
	if !ok {
		return false
	}
	approved, ok := fields["approved"].(bool)
	return ok && approved
}

func operationRequiresApproval(op *singularity.Operation) bool {
	return op == nil || op.Method != http.MethodGet
}

const approvalPreviewLimit = 500

func approvalMessage(toolName string, op *singularity.Operation, args map[string]any) string {
	operationName := ""
	method := ""
	path := ""
	if op != nil {
		operationName = op.Name
		method = op.Method
		path = op.Path
	}
	parts := []string{
		"Approve Singularity write operation?",
		"tool=" + toolName,
		"operation=" + operationName,
		"method=" + method,
		"path=" + path,
	}
	if id, ok := args["id"].(string); ok && id != "" {
		parts = append(parts, "id="+id)
	}
	if preview := approvalArgsPreview(args); preview != "" {
		parts = append(parts, "args="+preview)
	}
	if body, ok := args["body"]; ok {
		parts = append(parts, "body="+boundedPreview(body, approvalPreviewLimit))
	}
	return strings.Join(parts, "\n")
}

func approvalArgsPreview(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for key := range args {
		switch key {
		case "operation", "body", "confirm":
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	preview := make(map[string]any, len(keys))
	for _, key := range keys {
		preview[key] = args[key]
	}
	return boundedPreview(preview, approvalPreviewLimit)
}

func boundedPreview(value any, limit int) string {
	raw, err := json.Marshal(value)
	if err != nil {
		raw = fmt.Append(nil, value)
	}
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit]) + "…"
}

func (b Builder) readResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	_ = ctx
	switch req.Params.URI {
	case "singularity://openapi":
		return []mcp.ResourceContents{mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(openapi.Snapshot),
		}}, nil
	case "singularity://capabilities":
		raw, err := json.Marshal(capabilities(b.Catalog, b.RequireWriteApproval))
		if err != nil {
			return nil, err
		}
		return []mcp.ResourceContents{mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(raw),
		}}, nil
	default:
		return nil, fmt.Errorf("unknown resource URI: %s", req.Params.URI)
	}
}

func toolForGroup(group *singularity.ToolGroup) mcp.Tool {
	variants := make([]any, 0, len(group.Operations))
	for _, op := range group.Operations {
		variants = append(variants, schemaForOperation(op))
	}
	raw, err := json.Marshal(map[string]any{"type": "object", "oneOf": variants})
	if err != nil {
		panic(err)
	}
	tool := mcp.NewToolWithRawSchema(group.ToolName, toolDescription(group), raw)
	tool.RawOutputSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
	return tool
}

func schemaForOperation(op *singularity.Operation) map[string]any {
	props := map[string]any{"operation": map[string]any{"type": "string", "const": op.Name}}
	required := []string{"operation"}
	if op.Name == "get" || op.Name == "update" || op.Name == "delete" {
		props["id"] = map[string]any{"type": "string", "minLength": 1}
		required = append(required, "id")
	}
	if !isSyntheticTaskListOperation(op) {
		for _, param := range op.QueryParams {
			props[param.Name] = queryParamSchema(param)
			if param.Required {
				required = append(required, param.Name)
			}
		}
	}
	if op.BodySchema != nil {
		body := cloneSchema(op.BodySchema)
		body["description"] = "Create/update payload using exact Swagger field names."
		if _, ok := body["properties"].(map[string]any); ok {
			body["additionalProperties"] = false
		}
		decorateNoteBodySchema(op, body)
		props["body"] = body
		required = append(required, "body")
	}
	switch op.Name {
	case "delete":
		props["confirm"] = map[string]any{"type": "boolean", "const": true}
		required = append(required, "confirm")
	case "delete_bulk":
		props["confirm"] = map[string]any{"type": "string", "const": "DELETE"}
		required = append(required, "confirm")
	case "list":
		props["all"] = map[string]any{"type": "boolean", "default": false}
		props["compact"] = map[string]any{"type": "boolean", "default": false}
	case "overdue", "today", "only-today":
		props["compact"] = map[string]any{"type": "boolean", "default": false}
	case "search":
		props["all"] = map[string]any{"type": "boolean", "default": true}
		props["compact"] = map[string]any{"type": "boolean", "default": true}
		props["query"] = map[string]any{"type": "string"}
		fields := []string{"id", "title"}
		props["fields"] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": fields}}
		props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 20}
		if op.Tag == "task" {
			props["tag"] = map[string]any{"type": "string"}
			props["tags"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
			props["tagMode"] = map[string]any{"type": "string", "enum": []string{"any", "all"}, "default": "any"}
			props["checked"] = map[string]any{"type": "integer", "enum": []int{0, 1, 2}}
			props["priority"] = map[string]any{"type": "integer", "enum": []int{0, 1, 2}}
		}
		if op.Tag == "project" {
			props["isNotebook"] = map[string]any{"type": "boolean"}
			props["parent"] = map[string]any{"type": "string"}
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func isSyntheticTaskListOperation(op *singularity.Operation) bool {
	if op == nil || op.Tag != "task" {
		return false
	}
	switch op.Name {
	case "inbox", "overdue", "today", "only-today":
		return true
	default:
		return false
	}
}

func queryParamSchema(param singularity.Parameter) map[string]any {
	schema := cloneSchema(param.Schema)
	if schema == nil {
		schema = map[string]any{"type": "string"}
	}
	if param.Description != "" {
		schema["description"] = param.Description
	}
	if param.Name == "maxCount" || param.Name == "offset" {
		schema["type"] = "integer"
	}
	if param.Name == "startDateFrom" || param.Name == "startDateTo" {
		schema["format"] = "date-time"
		schema["description"] = "RFC3339 timestamp. The live Singularity API rejects date-only values."
	}
	return schema
}

func toolDescription(group *singularity.ToolGroup) string {
	reads := make([]string, 0, len(group.Operations))
	writes := make([]string, 0, len(group.Operations))
	for _, op := range group.Operations {
		if operationRequiresApproval(op) {
			writes = append(writes, op.Name)
		} else {
			reads = append(reads, op.Name)
		}
	}
	description := "Run Singularity " + group.Tag + " operations."
	if len(reads) > 0 {
		description += " Read: " + strings.Join(reads, ", ") + "."
	}
	if len(writes) > 0 {
		description += " Write: " + strings.Join(writes, ", ") + "."
	}
	return description
}

func operationNames(group *singularity.ToolGroup) []string {
	names := make([]string, 0, len(group.Operations))
	for _, op := range group.Operations {
		names = append(names, op.Name)
	}
	return names
}

func decorateNoteBodySchema(op *singularity.Operation, body map[string]any) {
	if op == nil || (op.Tag != "task" && op.Tag != "project") || (op.Name != "create" && op.Name != "update") {
		return
	}
	props, _ := body["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		body["properties"] = props
	}
	if note, ok := props["note"].(map[string]any); ok {
		note["description"] = "Plain text note. Do not pass JSON or Quill Delta; use noteText for clarity."
	}
	props["noteText"] = map[string]any{
		"type":        "string",
		"description": "Plain text note alias. The MCP server sends it to Singularity as note.",
	}
}

func capabilities(catalog *singularity.Catalog, requireWriteApproval bool) map[string]any {
	tools := make([]map[string]any, 0, len(catalog.Groups))
	for _, group := range catalog.Groups {
		tools = append(tools, map[string]any{
			"name":       group.ToolName,
			"operations": operationNames(group),
		})
	}
	return map[string]any{
		"tools":       tools,
		"omittedTags": catalog.OmittedTags,
		"operationSet": map[string]any{
			"swaggerTotal":   catalog.TotalOperations,
			"swaggerExposed": 41,
			"synthetic":      7,
			"omitted":        catalog.TotalOperations - 41,
			"exposedTotal":   catalog.ExposedOperationCount(),
		},
		"requireWriteApproval": requireWriteApproval,
	}
}

func cloneSchema(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneSchemaValue(value)
	}
	return out
}

func cloneSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSchema(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneSchemaValue(item)
		}
		return out
	default:
		return value
	}
}
