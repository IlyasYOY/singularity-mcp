package singularity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IlyasYOY/singularity-mcp/openapi"
)

func TestClientRequestEncoding(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_tasks", "create")

	var got struct {
		Method string
		Path   string
		Auth   string
		Body   map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Method = r.Method
		got.Path = r.URL.RequestURI()
		got.Auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got.Body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"T-1"}`))
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	raw, err := client.Call(context.Background(), op, map[string]any{
		"body": map[string]any{"title": "Task", "priority": float64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"id":"T-1"}` {
		t.Fatalf("raw = %s", raw)
	}
	if got.Method != "POST" || got.Path != "/v2/task" {
		t.Fatalf("request = %s %s", got.Method, got.Path)
	}
	if got.Auth != "Bearer secret-token" {
		t.Fatalf("auth = %q", got.Auth)
	}
	if got.Body["title"] != "Task" {
		t.Fatalf("body = %#v", got.Body)
	}
}

func TestClientNormalizesNoteText(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_tasks", "create")

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Write([]byte(`{"id":"T-1"}`))
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	_, err := client.Call(context.Background(), op, map[string]any{
		"body": map[string]any{"title": "Task", "noteText": "plain note"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["note"] != "plain note" {
		t.Fatalf("note = %#v", got["note"])
	}
	if _, ok := got["noteText"]; ok {
		t.Fatalf("noteText was sent: %#v", got)
	}
}

func TestClientConvertsDeltaNoteString(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_tasks", "create")

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Write([]byte(`{"id":"T-1"}`))
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	_, err := client.Call(context.Background(), op, map[string]any{
		"body": map[string]any{
			"title": "Task",
			"note":  `{"ops":[{"insert":"Line one\n"},{"insert":"Line two"}]}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["note"] != "Line one\nLine two" {
		t.Fatalf("note = %#v", got["note"])
	}
}

func TestClientRejectsConflictingNoteInputs(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_projects", "update")

	client := testClient(t, "https://api.example")
	_, err := client.Call(context.Background(), op, map[string]any{
		"id": "P-1",
		"body": map[string]any{
			"note":     "raw",
			"noteText": "plain",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "body.note and body.noteText") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientPathQueryAnd204(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_projects", "delete")

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		if r.Method != "DELETE" {
			t.Fatalf("method = %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	raw, err := client.Call(context.Background(), op, map[string]any{
		"id":      "P-a b",
		"confirm": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v2/project/P-a%20b" {
		t.Fatalf("path = %q", gotPath)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("raw = %s", raw)
	}
}

func TestClientListQuery(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_tasks", "list")

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	_, err := client.Call(context.Background(), op, map[string]any{
		"maxCount":                        float64(25),
		"offset":                          float64(5),
		"includeArchived":                 true,
		"includeAllRecurrenceInstances":   false,
		"projectId":                       "P-1",
		"unknownShouldNotBecomeAQueryKey": "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"maxCount=25",
		"offset=5",
		"includeArchived=true",
		"includeAllRecurrenceInstances=false",
		"projectId=P-1",
	} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query %q missing %s", gotQuery, want)
		}
	}
	if strings.Contains(gotQuery, "unknown") {
		t.Fatalf("query includes unknown key: %q", gotQuery)
	}
}

func TestClientCompactIgnoredForNonListOperations(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_tasks", "get")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"T-1","title":"Task","modificated":{"title":1}}`))
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	raw, err := client.Call(context.Background(), op, map[string]any{
		"id":      "T-1",
		"compact": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"id":"T-1","title":"Task","modificated":{"title":1}}` {
		t.Fatalf("raw = %s", raw)
	}
}

func TestClientValidation(t *testing.T) {
	catalog := testCatalog(t)
	update, _ := catalog.Operation("singularity_tags", "update")
	err := validateArgs(update, map[string]any{
		"id":   "A-1",
		"body": map[string]any{"color": "red"},
	})
	if err == nil || !strings.Contains(err.Error(), "body.title") {
		t.Fatalf("err = %v", err)
	}

	del, _ := catalog.Operation("singularity_tasks", "delete")
	err = validateArgs(del, map[string]any{"id": "T-1"})
	if err == nil || !strings.Contains(err.Error(), "confirm=true") {
		t.Fatalf("err = %v", err)
	}

	bulk, _ := catalog.Operation("singularity_time_stats", "delete_bulk")
	err = validateArgs(bulk, map[string]any{"confirm": "DELETE"})
	if err == nil || !strings.Contains(err.Error(), "at least one filter") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientMissingTokenFailsAtCallTime(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_projects", "list")
	client, err := NewAPIClient("https://api.example", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Call(context.Background(), op, nil)
	if err == nil || !strings.Contains(err.Error(), "SINGULARITY_TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientErrorRedaction(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_projects", "list")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"token":"secret-token","message":"bad"}`))
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	_, err := client.Call(context.Background(), op, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err type = %T", err)
	}
	if strings.Contains(apiErr.Response, "secret-token") {
		t.Fatalf("response not redacted: %s", apiErr.Response)
	}
	structured := StructuredError(err)
	if !strings.Contains(structured, `"status":401`) || strings.Contains(structured, "secret-token") {
		t.Fatalf("structured = %s", structured)
	}
}

func TestClientPagination(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_projects", "list")

	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offsets = append(offsets, r.URL.Query().Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("offset") {
		case "0":
			items := make([]map[string]string, PageSize)
			for i := range items {
				items[i] = map[string]string{"id": "P-full"}
			}
			json.NewEncoder(w).Encode(map[string]any{"projects": items})
		case "1000":
			w.Write([]byte(`{"projects":[{"id":"P-last"}]}`))
		default:
			t.Fatalf("unexpected offset %q", r.URL.Query().Get("offset"))
		}
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	raw, err := client.Call(context.Background(), op, map[string]any{"all": true})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Projects []map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Projects) != PageSize+1 {
		t.Fatalf("projects = %d", len(decoded.Projects))
	}
	if strings.Join(offsets, ",") != "0,1000" {
		t.Fatalf("offsets = %v", offsets)
	}
}

func TestClientInboxOperationFiltersAndCompactsTasks(t *testing.T) {
	catalog := testCatalog(t)
	op, ok := catalog.Operation("singularity_tasks", "inbox")
	if !ok {
		t.Fatal("inbox op missing")
	}

	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("offset") {
		case "0":
			items := make([]map[string]any, PageSize)
			for i := range items {
				items[i] = map[string]any{
					"id":           "P-task",
					"title":        "project task",
					"projectId":    "P-1",
					"modificated":  map[string]any{"title": float64(1)},
					"parentOrder":  float64(i),
					"createdDate":  "2026-01-01T00:00:00Z",
					"modifiedDate": "2026-01-01T00:00:00Z",
				}
			}
			items[10] = map[string]any{
				"id":          "T-inbox-1",
				"title":       "Inbox one",
				"modificated": map[string]any{"title": float64(1)},
				"parentOrder": float64(10),
				"tags":        []any{},
			}
			json.NewEncoder(w).Encode(map[string]any{"tasks": items})
		case "1000":
			json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{
				{"id": "T-inbox-2", "title": "Inbox two", "tags": []any{}},
				{"id": "T-subtask", "title": "Subtask", "parent": "T-parent"},
				{"id": "T-project", "title": "Project task", "projectId": "P-2"},
			}})
		default:
			t.Fatalf("unexpected offset %q", r.URL.Query().Get("offset"))
		}
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	raw, err := client.Call(context.Background(), op, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Count int `json:"count"`
		Tasks []struct {
			ID          string         `json:"id"`
			Title       string         `json:"title"`
			Modificated map[string]any `json:"modificated"`
			ProjectID   string         `json:"projectId"`
			Parent      string         `json:"parent"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Count != 2 || len(decoded.Tasks) != 2 {
		t.Fatalf("decoded = %#v", decoded)
	}
	if decoded.Tasks[0].ID != "T-inbox-1" || decoded.Tasks[1].ID != "T-inbox-2" {
		t.Fatalf("tasks = %#v", decoded.Tasks)
	}
	if decoded.Tasks[0].Modificated != nil || decoded.Tasks[0].ProjectID != "" || decoded.Tasks[0].Parent != "" {
		t.Fatalf("task was not compact/inbox-filtered: %#v", decoded.Tasks[0])
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "includeAllRecurrenceInstances=false") {
		t.Fatalf("queries = %v", queries)
	}
}

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := NewCatalog(openapi.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func testClient(t *testing.T, baseURL string) *APIClient {
	t.Helper()
	client, err := NewAPIClient(baseURL, "secret-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
