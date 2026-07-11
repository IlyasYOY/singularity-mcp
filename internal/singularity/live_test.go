package singularity

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/IlyasYOY/singularity-mcp/internal/config"
	"github.com/IlyasYOY/singularity-mcp/openapi"
)

func TestLiveReadOnlyListOperations(t *testing.T) {
	client, catalog := liveClientAndCatalog(t)
	for _, toolName := range []string{
		"singularity_projects",
		"singularity_task_groups",
		"singularity_tasks",
		"singularity_habits",
		"singularity_habit_progress",
		"singularity_checklist_items",
		"singularity_tags",
		"singularity_time_stats",
	} {
		t.Run(toolName, func(t *testing.T) {
			op, ok := catalog.Operation(toolName, "list")
			if !ok {
				t.Fatalf("%s list operation missing", toolName)
			}
			raw, err := client.Call(context.Background(), op, map[string]any{"maxCount": float64(1)})
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(raw) {
				t.Fatalf("invalid JSON: %s", raw)
			}
		})
	}
}

func TestLiveTaskDateOperations(t *testing.T) {
	client, catalog := liveClientAndCatalog(t)
	today := localDate(time.Now())
	for _, operation := range []string{"overdue", "today", "only-today"} {
		t.Run(operation, func(t *testing.T) {
			op, ok := catalog.Operation("singularity_tasks", operation)
			if !ok {
				t.Fatalf("task %s operation missing", operation)
			}
			raw, err := client.Call(context.Background(), op, map[string]any{"compact": true})
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Count int              `json:"count"`
				Tasks []map[string]any `json:"tasks"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if response.Count != len(response.Tasks) {
				t.Fatalf("count=%d tasks=%d", response.Count, len(response.Tasks))
			}
			for _, task := range response.Tasks {
				if !isTaskInDateList(operation, task, today) {
					t.Fatalf("%s returned task outside its contract: %s", operation, mustJSON(task))
				}
			}
		})
	}
}

func liveClientAndCatalog(t *testing.T) (*APIClient, *Catalog) {
	t.Helper()
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

	catalog, err := NewCatalog(openapi.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewAPIClient(baseURL, token, config.DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	return client, catalog
}
