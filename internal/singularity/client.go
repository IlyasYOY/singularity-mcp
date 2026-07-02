package singularity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type APIClient struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

type APIClientOption func(*APIClient)

func WithCustomHTTPClient(httpClient *http.Client) APIClientOption {
	return func(c *APIClient) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

func NewAPIClient(baseURL, token string, timeout time.Duration, opts ...APIClientOption) (*APIClient, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("base URL must include scheme and host: %q", baseURL)
	}
	client := &APIClient{
		baseURL: parsed,
		token:   token,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
	for _, opt := range opts {
		opt(client)
	}
	return client, nil
}

func (c *APIClient) Call(ctx context.Context, op *Operation, args map[string]any) ([]byte, error) {
	if op == nil {
		return nil, NewValidationError("operation is required")
	}
	if c.token == "" {
		return nil, NewValidationError("SINGULARITY_TOKEN is required for API calls")
	}
	if args == nil {
		args = map[string]any{}
	}
	normalizedArgs, err := normalizeArgs(op, args)
	if err != nil {
		return nil, err
	}
	args = normalizedArgs
	if err := validateArgs(op, args); err != nil {
		return nil, err
	}
	var raw []byte
	if op.Name == "inbox" || (op.Name == "list" && truthy(args["all"])) {
		raw, err = c.callAllPages(ctx, op, args)
	} else {
		raw, err = c.callOnce(ctx, op, args, nil)
	}
	if err != nil {
		return nil, err
	}
	return postProcessListResponse(op, args, raw)
}

func (c *APIClient) callAllPages(ctx context.Context, op *Operation, args map[string]any) ([]byte, error) {
	offset := intArg(args["offset"], 0)
	combined := map[string]any{}
	var listField string

	for {
		pageArgs := cloneArgs(args)
		pageArgs["maxCount"] = float64(PageSize)
		pageArgs["offset"] = float64(offset)
		delete(pageArgs, "all")
		if op.Name == "inbox" {
			if _, ok := pageArgs["includeAllRecurrenceInstances"]; !ok {
				pageArgs["includeAllRecurrenceInstances"] = false
			}
		}

		raw, err := c.callOnce(ctx, op, pageArgs, nil)
		if err != nil {
			return nil, err
		}
		var page map[string]any
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("decode paged response: %w", err)
		}
		if listField == "" {
			listField = op.ListResponseField
			if listField == "" {
				listField = firstArrayField(page)
			}
		}
		items, _ := page[listField].([]any)
		if _, ok := combined[listField]; !ok {
			combined[listField] = []any{}
		}
		combined[listField] = append(combined[listField].([]any), items...)
		if len(items) < PageSize {
			break
		}
		offset += len(items)
	}

	return marshalJSON(combined)
}

func postProcessListResponse(op *Operation, args map[string]any, raw []byte) ([]byte, error) {
	if op.Name != "inbox" && !(op.Name == "list" && truthy(args["compact"])) {
		return raw, nil
	}

	var page map[string]any
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}
	listField := op.ListResponseField
	if listField == "" {
		listField = firstArrayField(page)
	}
	items, _ := page[listField].([]any)
	out := make([]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if op.Name == "inbox" && !isInboxTask(m) {
			continue
		}
		if op.Name == "inbox" || truthy(args["compact"]) {
			out = append(out, compactEntity(m))
		} else {
			out = append(out, item)
		}
	}
	page[listField] = out
	page["count"] = len(out)
	return marshalJSON(page)
}

func isInboxTask(task map[string]any) bool {
	return emptyField(task, "projectId") && emptyField(task, "parent")
}

func emptyField(values map[string]any, key string) bool {
	value, ok := values[key]
	if !ok || value == nil {
		return true
	}
	s, ok := value.(string)
	return ok && s == ""
}

func compactEntity(item map[string]any) map[string]any {
	keep := []string{
		"id",
		"title",
		"projectId",
		"parent",
		"group",
		"start",
		"deadline",
		"priority",
		"state",
		"checked",
		"complete",
		"deferred",
		"showInBasket",
		"createdDate",
		"modificatedDate",
		"tags",
		"recurrenceGeneratorId",
	}
	out := make(map[string]any, len(keep))
	for _, key := range keep {
		if value, ok := item[key]; ok {
			out[key] = value
		}
	}
	return out
}

func normalizeArgs(op *Operation, args map[string]any) (map[string]any, error) {
	if !isNoteBodyOperation(op) {
		return args, nil
	}
	body, ok := args["body"].(map[string]any)
	if !ok {
		return args, nil
	}

	normalizedBody, err := normalizeNoteBody(body)
	if err != nil {
		return nil, err
	}
	normalizedArgs := cloneArgs(args)
	normalizedArgs["body"] = normalizedBody
	return normalizedArgs, nil
}

func isNoteBodyOperation(op *Operation) bool {
	if op == nil || (op.Tag != "task" && op.Tag != "project") {
		return false
	}
	return op.Name == "create" || op.Name == "update"
}

func normalizeNoteBody(body map[string]any) (map[string]any, error) {
	normalized := make(map[string]any, len(body))
	for key, value := range body {
		normalized[key] = value
	}

	noteText, hasNoteText := normalized["noteText"]
	note, hasNote := normalized["note"]
	if hasNoteText && hasNote {
		return nil, NewValidationError("body.note and body.noteText cannot both be provided")
	}
	if hasNoteText {
		text, ok := stringArg(noteText)
		if !ok {
			return nil, NewValidationError("body.noteText must be a string")
		}
		normalized["note"] = text
		delete(normalized, "noteText")
		return normalized, nil
	}
	if noteText, ok := stringArg(note); ok {
		if plain, ok := noteTextFromDeltaJSON(noteText); ok {
			normalized["note"] = plain
		}
	}
	return normalized, nil
}

func noteTextFromDeltaJSON(value string) (string, bool) {
	var delta struct {
		Ops []map[string]any `json:"ops"`
	}
	if err := json.Unmarshal([]byte(value), &delta); err != nil || len(delta.Ops) == 0 {
		return "", false
	}

	var out strings.Builder
	for _, op := range delta.Ops {
		insert, ok := op["insert"]
		if !ok {
			return "", false
		}
		text, ok := insert.(string)
		if !ok {
			return "", false
		}
		out.WriteString(text)
	}
	return out.String(), true
}

func (c *APIClient) callOnce(ctx context.Context, op *Operation, args map[string]any, overrideQuery url.Values) ([]byte, error) {
	endpoint, err := c.endpoint(op, args)
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	for _, param := range op.QueryParams {
		value, ok := args[param.Name]
		if !ok || value == nil {
			continue
		}
		query.Set(param.Name, formatQueryValue(value))
	}
	if overrideQuery != nil {
		for key, values := range overrideQuery {
			query.Del(key)
			for _, value := range values {
				query.Add(key, value)
			}
		}
	}
	endpoint.RawQuery = query.Encode()

	var body io.Reader
	if op.BodySchema != nil {
		raw, err := json.Marshal(args["body"])
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, op.Method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if op.BodySchema != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send %s %s: %w", op.Method, op.Path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, NewAPIError(resp.StatusCode, op.Method, op.Path, data, c.token)
	}
	if resp.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(data)) == 0 {
		return []byte(`{"ok":true}`), nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid JSON response from %s %s", op.Method, op.Path)
	}
	return compactJSON(data), nil
}

func (c *APIClient) endpoint(op *Operation, args map[string]any) (*url.URL, error) {
	path := op.Path
	rawPath := ""
	if strings.Contains(path, "{id}") {
		id, _ := stringArg(args["id"])
		path = strings.ReplaceAll(path, "{id}", id)
		rawPath = strings.ReplaceAll(op.Path, "{id}", url.PathEscape(id))
	}
	base := *c.baseURL
	prefix := strings.TrimRight(base.Path, "/")
	base.Path = prefix + path
	if rawPath != "" {
		base.RawPath = prefix + rawPath
	}
	base.RawQuery = ""
	base.Fragment = ""
	return &base, nil
}

func validateArgs(op *Operation, args map[string]any) error {
	if needsID(op) {
		id, ok := stringArg(args["id"])
		if !ok || id == "" {
			return NewValidationError("id is required for " + op.Name)
		}
	}
	if op.Name == "delete" {
		confirmed, ok := boolArg(args["confirm"])
		if !ok || !confirmed {
			return NewValidationError("confirm=true is required for delete")
		}
	}
	if op.Name == "delete_bulk" {
		confirm, _ := stringArg(args["confirm"])
		if confirm != "DELETE" {
			return NewValidationError(`confirm="DELETE" is required for delete_bulk`)
		}
		if !hasAnyQueryFilter(op, args) {
			return NewValidationError("at least one filter is required for delete_bulk")
		}
	}
	if op.BodySchema != nil {
		body, ok := args["body"].(map[string]any)
		if !ok {
			return NewValidationError("body object is required for " + op.Name)
		}
		for _, name := range op.BodyRequired {
			value, ok := body[name]
			if !ok || value == nil {
				return NewValidationError("body." + name + " is required")
			}
		}
	}
	return nil
}

func needsID(op *Operation) bool {
	return op.Name == "get" || op.Name == "update" || op.Name == "delete"
}

func hasAnyQueryFilter(op *Operation, args map[string]any) bool {
	for _, param := range op.QueryParams {
		if value, ok := args[param.Name]; ok && value != nil && formatQueryValue(value) != "" {
			return true
		}
	}
	return false
}

func StructuredError(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return string(mustJSON(map[string]any{
			"error": map[string]any{
				"type":     "api_error",
				"status":   apiErr.Status,
				"method":   apiErr.Method,
				"path":     apiErr.Path,
				"response": apiErr.Response,
			},
		}))
	}
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		return string(mustJSON(map[string]any{
			"error": map[string]any{
				"type":    "validation_error",
				"message": validationErr.Message,
			},
		}))
	}
	return string(mustJSON(map[string]any{
		"error": map[string]any{
			"type":    "client_error",
			"message": err.Error(),
		},
	}))
}

func compactJSON(data []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return data
	}
	return buf.Bytes()
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"error":{"type":"internal_error","message":"marshal error"}}`)
	}
	return data
}

func firstArrayField(page map[string]any) string {
	keys := make([]string, 0, len(page))
	for key := range page {
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		if _, ok := page[key].([]any); ok {
			return key
		}
	}
	return ""
}

func cloneArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	return out
}

func truthy(value any) bool {
	v, _ := boolArg(value)
	return v
}

func boolArg(value any) (bool, bool) {
	v, ok := value.(bool)
	return v, ok
}

func stringArg(value any) (string, bool) {
	v, ok := value.(string)
	return v, ok
}

func intArg(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, err := strconv.Atoi(v.String())
		if err == nil {
			return i
		}
	}
	return fallback
}

func formatQueryValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
