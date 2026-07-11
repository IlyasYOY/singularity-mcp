package tools

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/IlyasYOY/singularity-mcp/internal/config"
	"github.com/IlyasYOY/singularity-mcp/internal/singularity"
	"github.com/IlyasYOY/singularity-mcp/openapi"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestLiveTodayToolCall(t *testing.T) {
	if os.Getenv("SINGULARITY_LIVE") != "1" {
		t.Skip("set SINGULARITY_LIVE=1 to run live read-only tests")
	}
	token := os.Getenv("SINGULARITY_TOKEN")
	if token == "" {
		t.Fatal("SINGULARITY_TOKEN is required for live tests")
	}
	baseURL := os.Getenv("SINGULARITY_BASE_URL")
	if baseURL == "" {
		baseURL = config.DefaultBaseURL
	}

	catalog, err := singularity.NewCatalog(openapi.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	apiClient, err := singularity.NewAPIClient(baseURL, token, config.DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithOptions(apiClient, catalog, "live-test", Options{RequireWriteApproval: false})
	mcpClient, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpClient.Close()
	startClient(t, mcpClient)

	req := mcp.CallToolRequest{}
	req.Params.Name = "singularity_tasks"
	req.Params.Arguments = map[string]any{"operation": "today", "compact": true}
	result, err := mcpClient.CallTool(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("today tool call failed: %s", resultText(result))
	}
	var response struct {
		Count int              `json:"count"`
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(resultText(result)), &response); err != nil {
		t.Fatal(err)
	}
	if response.Count != len(response.Tasks) {
		t.Fatalf("count=%d tasks=%d", response.Count, len(response.Tasks))
	}
}
