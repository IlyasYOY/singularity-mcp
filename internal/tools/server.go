package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/IlyasYOY/singularity-mcp/internal/singularity"
	"github.com/IlyasYOY/singularity-mcp/openapi"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Builder struct {
	Client  *singularity.APIClient
	Catalog *singularity.Catalog
}

func NewServer(client *singularity.APIClient, catalog *singularity.Catalog, version string) *server.MCPServer {
	mcpServer := server.NewMCPServer(
		"singularity-mcp",
		version,
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithRecovery(),
	)
	builder := Builder{Client: client, Catalog: catalog}
	builder.Register(mcpServer)
	return mcpServer
}

func (b Builder) Register(mcpServer *server.MCPServer) {
	for _, group := range b.Catalog.Groups {
		group := group
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
	args := req.GetArguments()
	operationName, ok := args["operation"].(string)
	if !ok || operationName == "" {
		return mcp.NewToolResultError(singularity.StructuredError(singularity.NewValidationError("operation is required"))), nil
	}
	op, ok := b.Catalog.Operation(group.ToolName, operationName)
	if !ok {
		return mcp.NewToolResultError(singularity.StructuredError(singularity.NewValidationError("invalid operation: " + operationName))), nil
	}
	raw, err := b.Client.Call(ctx, op, args)
	if err != nil {
		return mcp.NewToolResultError(singularity.StructuredError(err)), nil
	}
	return mcp.NewToolResultText(string(raw)), nil
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
		raw, err := json.Marshal(capabilities(b.Catalog))
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
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"operation"},
		"properties": map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"description": "Operation to run.",
				"enum":        operationNames(group),
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Entity ID. Required for get, update, and delete.",
			},
			"body": map[string]any{
				"type":                 "object",
				"description":          "Create/update payload using exact Swagger field names.",
				"additionalProperties": true,
			},
			"confirm": map[string]any{
				"description": "Use true for single delete, or DELETE for time_stats.delete_bulk.",
				"oneOf": []map[string]any{
					{"type": "boolean"},
					{"type": "string", "enum": []string{"DELETE"}},
				},
			},
			"all": map[string]any{
				"type":        "boolean",
				"description": "For list operations, fetch all pages using maxCount=1000.",
				"default":     false,
			},
			"compact": map[string]any{
				"type":        "boolean",
				"description": "For list operations, return compact entities without large metadata fields.",
				"default":     false,
			},
		},
	}
	props := schema["properties"].(map[string]any)
	for _, op := range group.Operations {
		for _, param := range op.QueryParams {
			if _, exists := props[param.Name]; exists {
				continue
			}
			props[param.Name] = queryParamSchema(param)
		}
		if op.BodySchema != nil {
			body := cloneSchema(op.BodySchema)
			body["description"] = "Create/update payload using exact Swagger field names."
			body["additionalProperties"] = true
			decorateNoteBodySchema(op, body)
			props["body"] = body
		}
	}

	raw, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return mcp.NewToolWithRawSchema(group.ToolName, toolDescription(group), raw)
}

func queryParamSchema(param singularity.Parameter) map[string]any {
	schema := cloneSchema(param.Schema)
	if schema == nil {
		schema = map[string]any{"type": "string"}
	}
	if param.Description != "" {
		schema["description"] = param.Description
	}
	return schema
}

func toolDescription(group *singularity.ToolGroup) string {
	return "Run Singularity " + group.Tag + " operations."
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

func capabilities(catalog *singularity.Catalog) map[string]any {
	tools := make([]map[string]any, 0, len(catalog.Groups))
	for _, group := range catalog.Groups {
		tools = append(tools, map[string]any{
			"name":       group.ToolName,
			"operations": operationNames(group),
		})
	}
	return map[string]any{
		"tools":        tools,
		"omittedTags":  catalog.OmittedTags,
		"operationSet": map[string]any{"totalSwagger": catalog.TotalOperations, "exposed": catalog.ExposedOperationCount()},
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
