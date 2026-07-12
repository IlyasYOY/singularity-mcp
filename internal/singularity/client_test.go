package singularity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		Count    int              `json:"count"`
		Projects []map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Count != PageSize+1 || len(decoded.Projects) != PageSize+1 {
		t.Fatalf("count = %d, projects = %d", decoded.Count, len(decoded.Projects))
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

func TestClientTaskDateOperationsFilterActiveTasks(t *testing.T) {
	catalog := testCatalog(t)
	moscow := time.FixedZone("Europe/Moscow", 3*60*60)
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, moscow)

	tests := []struct {
		name              string
		operation         string
		wantIDs           []string
		wantStartDateFrom string
		wantStartDateTo   string
	}{
		{
			name:              "overdue",
			operation:         "overdue",
			wantIDs:           []string{"T-overdue", "T-overdue-datetime", "T-overdue-sparse"},
			wantStartDateFrom: "",
			wantStartDateTo:   "2026-07-04T00:00:00+03:00",
		},
		{
			name:              "today",
			operation:         "today",
			wantIDs:           []string{"T-overdue", "T-overdue-datetime", "T-overdue-sparse", "T-today", "T-today-utc-boundary"},
			wantStartDateFrom: "",
			wantStartDateTo:   "2026-07-05T00:00:00+03:00",
		},
		{
			name:              "only today",
			operation:         "only-today",
			wantIDs:           []string{"T-today", "T-today-utc-boundary"},
			wantStartDateFrom: "2026-07-04T00:00:00+03:00",
			wantStartDateTo:   "2026-07-05T00:00:00+03:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op, ok := catalog.Operation("singularity_tasks", tt.operation)
			if !ok {
				t.Fatalf("%s op missing", tt.operation)
			}

			var gotQuery string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.RawQuery
				for _, name := range []string{"startDateFrom", "startDateTo"} {
					if value := r.URL.Query().Get(name); value != "" {
						if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
							http.Error(w, name+" must be an ISOString", http.StatusBadRequest)
							return
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"tasks": taskDateFixture()})
			}))
			defer srv.Close()

			client := testClientAt(t, srv.URL, now)
			raw, err := client.Call(context.Background(), op, map[string]any{
				"compact":                       false,
				"includeArchived":               true,
				"maxCount":                      float64(1),
				"offset":                        float64(25),
				"projectId":                     "P-1",
				"startDateFrom":                 "2020-01-01",
				"startDateTo":                   "2099-01-01",
				"includeAllRecurrenceInstances": true,
			})
			if err != nil {
				t.Fatal(err)
			}

			query, err := url.ParseQuery(gotQuery)
			if err != nil {
				t.Fatal(err)
			}
			if query.Get("maxCount") != "1000" || query.Get("offset") != "0" {
				t.Fatalf("pagination query = %s", gotQuery)
			}
			if query.Get("projectId") != "P-1" || query.Get("includeArchived") != "true" || query.Get("includeAllRecurrenceInstances") != "true" {
				t.Fatalf("filters query = %s", gotQuery)
			}
			if query.Get("startDateFrom") != tt.wantStartDateFrom || query.Get("startDateTo") != tt.wantStartDateTo {
				t.Fatalf("date query = %s", gotQuery)
			}

			var decoded struct {
				Count int              `json:"count"`
				Tasks []map[string]any `json:"tasks"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Count != len(tt.wantIDs) {
				t.Fatalf("count = %d, tasks = %#v", decoded.Count, decoded.Tasks)
			}
			if got := taskIDs(decoded.Tasks); strings.Join(got, ",") != strings.Join(tt.wantIDs, ",") {
				t.Fatalf("tasks = %v, want %v", got, tt.wantIDs)
			}
		})
	}
}

func TestClientRejectsInvalidTaskDateFiltersBeforeHTTP(t *testing.T) {
	catalog := testCatalog(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	for _, operation := range []string{"list", "search"} {
		op, _ := catalog.Operation("singularity_tasks", operation)
		args := map[string]any{"startDateFrom": "2026-07-04"}
		if operation == "search" {
			args["query"] = "contract"
		}
		_, err := client.Call(context.Background(), op, args)
		if err == nil || !strings.Contains(err.Error(), "startDateFrom must be an RFC3339 timestamp") {
			t.Fatalf("%s error = %v", operation, err)
		}
	}
	if calls != 0 {
		t.Fatalf("HTTP calls = %d", calls)
	}
}

func TestTaskDateBoundariesHonorLocationAndDST(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	today := localDate(time.Date(2026, 3, 29, 12, 0, 0, 0, berlin))
	args := taskDateListArgs("only-today", nil, today)
	if args["startDateFrom"] != "2026-03-29T00:00:00+01:00" || args["startDateTo"] != "2026-03-30T00:00:00+02:00" {
		t.Fatalf("DST boundaries = %#v", args)
	}
	date, ok := taskStartDate(map[string]any{"start": "2026-03-28T23:00:00Z"}, berlin)
	if !ok || !date.Equal(today) {
		t.Fatalf("parsed date = %s, ok = %v, today = %s", date, ok, today)
	}
}

func TestClientTaskDateOperationsSupportCompact(t *testing.T) {
	catalog := testCatalog(t)
	op, ok := catalog.Operation("singularity_tasks", "today")
	if !ok {
		t.Fatal("today op missing")
	}
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.Local)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{
			{
				"id":              "T-today",
				"title":           "Today",
				"start":           "2026-07-04",
				"checked":         float64(0),
				"removed":         false,
				"modificated":     map[string]any{"title": float64(1)},
				"parentOrder":     float64(10),
				"modificatedDate": "2026-07-04T09:00:00+03:00",
			},
		}})
	}))
	defer srv.Close()

	client := testClientAt(t, srv.URL, now)
	raw, err := client.Call(context.Background(), op, map[string]any{"compact": true})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Count int              `json:"count"`
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Count != 1 || decoded.Tasks[0]["id"] != "T-today" || decoded.Tasks[0]["start"] != "2026-07-04" {
		t.Fatalf("decoded = %#v", decoded)
	}
	if _, ok := decoded.Tasks[0]["modificated"]; ok {
		t.Fatalf("task was not compacted: %#v", decoded.Tasks[0])
	}
	if _, ok := decoded.Tasks[0]["parentOrder"]; ok {
		t.Fatalf("task was not compacted: %#v", decoded.Tasks[0])
	}
}

func TestClientSearchTasksFiltersByTitleAndCompacts(t *testing.T) {
	catalog := testCatalog(t)
	op, ok := catalog.Operation("singularity_tasks", "search")
	if !ok {
		t.Fatal("task search op missing")
	}

	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offsets = append(offsets, r.URL.Query().Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("offset") {
		case "0":
			items := make([]map[string]any, PageSize)
			for i := range items {
				items[i] = map[string]any{"id": "T-other", "title": "Other", "modificated": map[string]any{"title": 1}}
			}
			items[3] = map[string]any{"id": "T-mcp-1", "title": "MCP search", "projectId": "P-1", "modificated": map[string]any{"title": 1}}
			json.NewEncoder(w).Encode(map[string]any{"tasks": items})
		case "1000":
			json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{
				{"id": "T-mcp-2", "title": "Improve mcp", "tags": []string{"TG-1"}},
				{"id": "T-nope", "title": "Nope"},
			}})
		default:
			t.Fatalf("unexpected offset %q", r.URL.Query().Get("offset"))
		}
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	raw, err := client.Call(context.Background(), op, map[string]any{"query": "mcp", "limit": float64(10)})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Count int              `json:"count"`
		Tasks []map[string]any `json:"tasks"`
		Query map[string]any   `json:"query"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Count != 2 || strings.Join(taskIDs(decoded.Tasks), ",") != "T-mcp-1,T-mcp-2" {
		t.Fatalf("decoded = %#v", decoded)
	}
	if _, ok := decoded.Tasks[0]["modificated"]; ok {
		t.Fatalf("task was not compacted: %#v", decoded.Tasks[0])
	}
	if strings.Join(offsets, ",") != "0,1000" {
		t.Fatalf("offsets = %v", offsets)
	}
	if truncated, _ := decoded.Query["truncated"].(bool); truncated {
		t.Fatalf("unexpected truncated metadata: %#v", decoded.Query)
	}
}

func TestClientSearchTasksForwardsServerFiltersOnly(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_tasks", "search")

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	_, err := client.Call(context.Background(), op, map[string]any{
		"query":                         "mcp",
		"fields":                        []any{"title"},
		"limit":                         float64(5),
		"tagMode":                       "all",
		"projectId":                     "P-1",
		"parent":                        "T-parent",
		"startDateFrom":                 "2026-01-01T00:00:00Z",
		"startDateTo":                   "2026-02-01T00:00:00Z",
		"includeArchived":               true,
		"includeAllRecurrenceInstances": false,
		"all":                           false,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"projectId=P-1", "parent=T-parent", "startDateFrom=2026-01-01T00%3A00%3A00Z", "startDateTo=2026-02-01T00%3A00%3A00Z", "includeArchived=true", "includeAllRecurrenceInstances=false"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query %q missing %s", gotQuery, want)
		}
	}
	for _, unwanted := range []string{"query=", "fields=", "limit=", "tagMode=", "all="} {
		if strings.Contains(gotQuery, unwanted) {
			t.Fatalf("query %q contains search-only arg %s", gotQuery, unwanted)
		}
	}
}

func TestClientSearchTasksFiltersTagsAnyAll(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_tasks", "search")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{
			{"id": "T-1", "title": "Tagged", "tags": []any{"TG-1", "TG-2"}},
			{"id": "T-2", "title": "Tagged", "tags": []any{"TG-1"}},
			{"id": "T-3", "title": "Tagged", "tags": []any{"TG-3"}},
		}})
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	decodeIDs := func(args map[string]any) string {
		t.Helper()
		raw, err := client.Call(context.Background(), op, args)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Tasks []map[string]any `json:"tasks"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		return strings.Join(taskIDs(decoded.Tasks), ",")
	}
	if got := decodeIDs(map[string]any{"query": "tagged", "tags": []any{"TG-2", "TG-3"}, "tagMode": "any"}); got != "T-1,T-3" {
		t.Fatalf("any IDs = %s", got)
	}
	if got := decodeIDs(map[string]any{"query": "tagged", "tags": []any{"TG-1", "TG-2"}, "tagMode": "all"}); got != "T-1" {
		t.Fatalf("all IDs = %s", got)
	}
}

func TestClientSearchProjectsAndTags(t *testing.T) {
	catalog := testCatalog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/project":
			json.NewEncoder(w).Encode(map[string]any{"projects": []map[string]any{
				{"id": "P-1", "title": "Hermes", "isNotebook": false, "modificated": map[string]any{"title": 1}},
				{"id": "P-2", "title": "Other", "isNotebook": false},
			}})
		case "/v2/tag":
			json.NewEncoder(w).Encode(map[string]any{"tags": []map[string]any{
				{"id": "TG-1", "title": "work"},
				{"id": "TG-2", "title": "home"},
			}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	projectSearch, _ := catalog.Operation("singularity_projects", "search")
	raw, err := client.Call(context.Background(), projectSearch, map[string]any{"query": "hermes", "isNotebook": false})
	if err != nil {
		t.Fatal(err)
	}
	var projects struct {
		Projects []map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(raw, &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects.Projects) != 1 || projects.Projects[0]["id"] != "P-1" {
		t.Fatalf("projects = %#v", projects.Projects)
	}
	if _, ok := projects.Projects[0]["modificated"]; ok {
		t.Fatalf("project was not compacted: %#v", projects.Projects[0])
	}

	tagSearch, _ := catalog.Operation("singularity_tags", "search")
	raw, err = client.Call(context.Background(), tagSearch, map[string]any{"query": "work"})
	if err != nil {
		t.Fatal(err)
	}
	var tags struct {
		Tags []map[string]any `json:"tags"`
	}
	if err := json.Unmarshal(raw, &tags); err != nil {
		t.Fatal(err)
	}
	if len(tags.Tags) != 1 || tags.Tags[0]["id"] != "TG-1" {
		t.Fatalf("tags = %#v", tags.Tags)
	}
}

func TestClientSearchValidation(t *testing.T) {
	catalog := testCatalog(t)
	taskSearch, _ := catalog.Operation("singularity_tasks", "search")
	projectSearch, _ := catalog.Operation("singularity_projects", "search")
	tagSearch, _ := catalog.Operation("singularity_tags", "search")
	client := testClient(t, "https://api.example")
	tests := []struct {
		name string
		op   *Operation
		args map[string]any
		want string
	}{
		{"empty criteria", taskSearch, map[string]any{}, "query or at least one search filter"},
		{"bad field", taskSearch, map[string]any{"query": "x", "fields": []any{"bad"}}, "unsupported search field"},
		{"note unsupported for tasks", taskSearch, map[string]any{"query": "x", "fields": []any{"note"}}, "unsupported search field"},
		{"note unsupported for projects", projectSearch, map[string]any{"query": "x", "fields": []any{"note"}}, "unsupported search field"},
		{"note unsupported for tags", tagSearch, map[string]any{"query": "x", "fields": []any{"note"}}, "unsupported search field"},
		{"bad tag mode", taskSearch, map[string]any{"query": "x", "tagMode": "nope"}, "tagMode"},
		{"limit low", taskSearch, map[string]any{"query": "x", "limit": float64(0)}, "limit"},
		{"limit high", taskSearch, map[string]any{"query": "x", "limit": float64(101)}, "limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.Call(context.Background(), tt.op, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestClientSearchAllFalseSinglePage(t *testing.T) {
	catalog := testCatalog(t)
	op, _ := catalog.Operation("singularity_tasks", "search")

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		items := make([]map[string]any, PageSize)
		for i := range items {
			items[i] = map[string]any{"id": "T-match", "title": "mcp"}
		}
		json.NewEncoder(w).Encode(map[string]any{"tasks": items})
	}))
	defer srv.Close()

	client := testClient(t, srv.URL)
	raw, err := client.Call(context.Background(), op, map[string]any{"query": "mcp", "all": false, "limit": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Count int            `json:"count"`
		Query map[string]any `json:"query"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || decoded.Count != 1 {
		t.Fatalf("calls=%d decoded=%#v", calls, decoded)
	}
	if truncated, _ := decoded.Query["truncated"].(bool); !truncated {
		t.Fatalf("expected truncated metadata: %#v", decoded.Query)
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

func testClientAt(t *testing.T, baseURL string, now time.Time) *APIClient {
	t.Helper()
	client, err := NewAPIClient(baseURL, "secret-token", time.Second, WithClock(func() time.Time {
		return now
	}))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func taskDateFixture() []map[string]any {
	return []map[string]any{
		{"id": "T-overdue", "title": "Overdue", "start": "2026-07-03", "checked": float64(0), "removed": false},
		{"id": "T-overdue-datetime", "title": "Overdue datetime", "start": "2026-07-02T08:00:00+03:00", "checked": float64(0), "removed": false},
		{"id": "T-overdue-sparse", "title": "Overdue sparse", "start": "2026-07-03T09:00:00+03:00"},
		{"id": "T-today", "title": "Today", "start": "2026-07-04T09:00:00+03:00", "checked": float64(0), "removed": false},
		{"id": "T-today-utc-boundary", "title": "Today UTC boundary", "start": "2026-07-03T21:00:00Z"},
		{"id": "T-future", "title": "Future", "start": "2026-07-05", "checked": float64(0), "removed": false},
		{"id": "T-done", "title": "Done", "start": "2026-07-03", "checked": float64(1), "removed": false},
		{"id": "T-cancelled", "title": "Cancelled", "start": "2026-07-04", "checked": float64(2), "removed": false},
		{"id": "T-removed", "title": "Removed", "start": "2026-07-03", "checked": float64(0), "removed": true},
		{"id": "T-missing-start", "title": "Missing start", "checked": float64(0), "removed": false},
		{"id": "T-complete-field", "title": "Complete field", "start": "2026-07-03", "checked": float64(0), "complete": float64(100), "removed": false},
	}
}

func taskIDs(tasks []map[string]any) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		id, _ := task["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

func TestAPIClientForTokenReturnsIndependentView(t *testing.T) {
	base, err := NewAPIClient("https://api.example", "base-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	first := base.ForToken("first-token")
	second := base.ForToken("second-token")
	if base.token != "base-token" || first.token != "first-token" || second.token != "second-token" {
		t.Fatalf("tokens base=%q first=%q second=%q", base.token, first.token, second.token)
	}
	if first == base || second == base || first.httpClient != base.httpClient || second.httpClient != base.httpClient {
		t.Fatal("ForToken did not create independent views sharing the HTTP client")
	}
}
