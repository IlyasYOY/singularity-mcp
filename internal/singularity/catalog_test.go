package singularity

import (
	"testing"

	"github.com/IlyasYOY/singularity-mcp/openapi"
)

func TestCatalogCoverage(t *testing.T) {
	catalog, err := NewCatalog(openapi.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.TotalOperations != 51 {
		t.Fatalf("total ops = %d", catalog.TotalOperations)
	}
	if catalog.ExposedOperationCount() != 48 {
		t.Fatalf("exposed ops = %d", catalog.ExposedOperationCount())
	}
	if len(catalog.Groups) != 8 {
		t.Fatalf("groups = %d", len(catalog.Groups))
	}
	for _, name := range []string{"kanban-status", "kanban-task-status"} {
		found := false
		for _, omitted := range catalog.OmittedTags {
			found = found || omitted == name
		}
		if !found {
			t.Fatalf("omitted tags missing %s: %v", name, catalog.OmittedTags)
		}
	}
	if _, ok := catalog.Group("singularity_kanban_statuses"); ok {
		t.Fatal("kanban group exposed")
	}
	for _, tool := range []string{"singularity_tasks", "singularity_projects", "singularity_tags"} {
		search, ok := catalog.Operation(tool, "search")
		if !ok {
			t.Fatalf("%s search op missing", tool)
		}
		list, _ := catalog.Operation(tool, "list")
		if search.Method != list.Method || search.Path != list.Path || search.ListResponseField != list.ListResponseField {
			t.Fatalf("%s search = %#v, list = %#v", tool, search, list)
		}
	}
}

func TestCatalogOperationDetails(t *testing.T) {
	catalog, err := NewCatalog(openapi.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	op, ok := catalog.Operation("singularity_tasks", "list")
	if !ok {
		t.Fatal("task list op missing")
	}
	if op.Method != "GET" || op.Path != "/v2/task" {
		t.Fatalf("task list = %s %s", op.Method, op.Path)
	}
	if op.ListResponseField != "tasks" {
		t.Fatalf("list field = %q", op.ListResponseField)
	}
	for _, name := range []string{"inbox", "overdue", "today", "only-today"} {
		taskOp, ok := catalog.Operation("singularity_tasks", name)
		if !ok {
			t.Fatalf("task %s op missing", name)
		}
		if taskOp.Method != op.Method || taskOp.Path != op.Path || taskOp.ListResponseField != op.ListResponseField {
			t.Fatalf("task %s = %#v", name, taskOp)
		}
	}

	create, ok := catalog.Operation("singularity_habit_progress", "create")
	if !ok {
		t.Fatal("habit progress create op missing")
	}
	want := []string{"habit", "date", "progress"}
	if len(create.BodyRequired) != len(want) {
		t.Fatalf("required = %v", create.BodyRequired)
	}
	for i := range want {
		if create.BodyRequired[i] != want[i] {
			t.Fatalf("required = %v", create.BodyRequired)
		}
	}
}
