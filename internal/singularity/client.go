package singularity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type APIClient struct {
	baseURL          *url.URL
	token            string
	httpClient       *http.Client
	now              func() time.Time
	sleep            func(context.Context, time.Duration) error
	maxResponseBytes int64
	maxPages         int
	maxItems         int
}

type APIClientOption func(*APIClient)

func WithMaxResponseBytes(limit int64) APIClientOption {
	return func(c *APIClient) {
		if limit > 0 {
			c.maxResponseBytes = limit
		}
	}
}
func WithPaginationLimits(maxPages, maxItems int) APIClientOption {
	return func(c *APIClient) {
		if maxPages > 0 {
			c.maxPages = maxPages
		}
		if maxItems > 0 {
			c.maxItems = maxItems
		}
	}
}

func WithCustomHTTPClient(httpClient *http.Client) APIClientOption {
	return func(c *APIClient) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

func WithClock(now func() time.Time) APIClientOption {
	return func(c *APIClient) {
		if now != nil {
			c.now = now
		}
	}
}

func WithSleeper(sleep func(context.Context, time.Duration) error) APIClientOption {
	return func(c *APIClient) {
		if sleep != nil {
			c.sleep = sleep
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
		now: time.Now,
		sleep: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		maxResponseBytes: 1 << 20,
		maxPages:         100,
		maxItems:         10000,
	}
	for _, opt := range opts {
		opt(client)
	}
	return client, nil
}

type PreparedCall struct {
	Operation *Operation
	Args      map[string]any
}

func (c *APIClient) PrepareCall(op *Operation, args map[string]any) (*PreparedCall, error) {
	if op == nil {
		return nil, NewValidationError("operation is required")
	}
	if c.token == "" {
		return nil, NewValidationError("SINGULARITY_TOKEN is required for API calls")
	}
	if args == nil {
		args = map[string]any{}
	}
	normalizedArgs, err := normalizeArgs(op, cloneArgsDeep(args))
	if err != nil {
		return nil, err
	}
	if err := validateArgs(op, normalizedArgs); err != nil {
		return nil, err
	}
	return &PreparedCall{Operation: op, Args: normalizedArgs}, nil
}

func (c *APIClient) Call(ctx context.Context, op *Operation, args map[string]any) ([]byte, error) {
	prepared, err := c.PrepareCall(op, args)
	if err != nil {
		return nil, err
	}
	return c.ExecutePrepared(ctx, prepared)
}

func (c *APIClient) ExecutePrepared(ctx context.Context, prepared *PreparedCall) ([]byte, error) {
	if prepared == nil || prepared.Operation == nil {
		return nil, NewValidationError("prepared call is required")
	}
	op, args := prepared.Operation, prepared.Args
	if op.Name == "search" {
		return c.search(ctx, op, args)
	}
	var raw []byte
	var err error
	today := localDate(c.now())
	if isTaskDateListOperation(op.Name) {
		args = taskDateListArgs(op.Name, args, today)
		raw, err = c.callAllPages(ctx, op, args)
	} else if op.Name == "inbox" || (op.Name == "list" && truthy(args["all"])) {
		raw, err = c.callAllPages(ctx, op, args)
	} else {
		raw, err = c.callOnce(ctx, op, args, nil)
	}
	if err != nil {
		return nil, err
	}
	return postProcessListResponse(op, args, raw, today)
}

func (c *APIClient) callAllPages(ctx context.Context, op *Operation, args map[string]any) ([]byte, error) {
	combined := map[string]any{}
	listField := op.ListResponseField
	stats, err := c.iteratePages(ctx, op, args, true, func(items []any) (int, int, bool, error) {
		if _, ok := combined[listField]; !ok {
			combined[listField] = []any{}
		}
		combined[listField] = append(combined[listField].([]any), items...)
		return len(items), len(items), false, nil
	})
	if err != nil {
		return nil, err
	}
	combined["count"] = len(combined[listField].([]any))
	if stats.MorePossible {
		combined["pagination"] = map[string]any{"scannedPages": stats.ScannedPages, "scannedItems": stats.ScannedItems, "truncated": true, "nextOffset": stats.NextOffset, "reason": stats.StopReason}
	}
	return marshalJSON(combined)
}

type pageConsumer func(items []any) (scanned int, advance int, stop bool, err error)

func (c *APIClient) iteratePages(ctx context.Context, op *Operation, args map[string]any, allPages bool, consume pageConsumer) (PageStats, error) {
	offset := intArg(args["offset"], 0)
	requestedPageSize := intArg(args["maxCount"], PageSize)
	if requestedPageSize <= 0 || requestedPageSize > PageSize {
		requestedPageSize = PageSize
	}
	stats := PageStats{NextOffset: offset}
	seen := map[[32]byte]struct{}{}
	listField := op.ListResponseField
	complete := false
	for stats.ScannedPages < c.maxPages && stats.ScannedItems < c.maxItems {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		pageSize := requestedPageSize
		if remaining := c.maxItems - stats.ScannedItems; remaining < pageSize {
			pageSize = remaining
		}
		pageArgs := cloneArgs(args)
		pageArgs["maxCount"] = float64(pageSize)
		pageArgs["offset"] = float64(offset)
		delete(pageArgs, "all")
		if op.Name == "inbox" {
			if _, ok := pageArgs["includeAllRecurrenceInstances"]; !ok {
				pageArgs["includeAllRecurrenceInstances"] = false
			}
		}
		raw, err := c.callOnce(ctx, op, pageArgs, nil)
		if err != nil {
			return stats, err
		}
		var page map[string]any
		if err := json.Unmarshal(raw, &page); err != nil {
			return stats, fmt.Errorf("decode paged response: %w", err)
		}
		if listField == "" {
			listField = firstArrayField(page)
		}
		items, _ := page[listField].([]any)
		if len(items) > pageSize {
			items = items[:pageSize]
		}
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if len(items) > 0 {
			canonical, _ := json.Marshal(items)
			fingerprint := sha256.Sum256(canonical)
			if _, ok := seen[fingerprint]; ok {
				return stats, &ClientError{Code: "pagination_stalled", Message: "pagination stalled on a repeated page", Metadata: map[string]any{"offset": offset}}
			}
			seen[fingerprint] = struct{}{}
		}
		stats.ScannedPages++
		scanned, advance, stop, err := consume(items)
		if err != nil {
			return stats, err
		}
		if scanned < 0 || scanned > len(items) {
			return stats, fmt.Errorf("invalid page scanned count %d", scanned)
		}
		if advance < 0 || advance > scanned {
			return stats, fmt.Errorf("invalid page advance count %d for %d scanned items", advance, scanned)
		}
		stats.ScannedItems += scanned
		offset += advance
		stats.NextOffset = offset
		if stop {
			stats.MorePossible, stats.StopReason = true, "consumer_limit"
			break
		}
		if len(items) < pageSize {
			complete = true
			break
		}
		if !allPages {
			stats.MorePossible, stats.StopReason = true, "single_page"
			break
		}
	}
	if !complete && !stats.MorePossible && stats.ScannedItems >= c.maxItems {
		stats.StopReason, stats.MorePossible = "max_items", true
	} else if !complete && !stats.MorePossible && stats.ScannedPages >= c.maxPages {
		stats.StopReason, stats.MorePossible = "max_pages", true
	}
	return stats, nil
}

type PageStats struct {
	ScannedPages int
	ScannedItems int
	NextOffset   int
	MorePossible bool
	StopReason   string
}

func postProcessListResponse(op *Operation, args map[string]any, raw []byte, today time.Time) ([]byte, error) {
	if op.Name != "inbox" && !isTaskDateListOperation(op.Name) && !(op.Name == "list" && truthy(args["compact"])) {
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
		if isTaskDateListOperation(op.Name) && !isTaskInDateList(op.Name, m, today) {
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

func isTaskDateListOperation(name string) bool {
	switch name {
	case "overdue", "today", "only-today":
		return true
	default:
		return false
	}
}

func taskDateListArgs(operation string, args map[string]any, today time.Time) map[string]any {
	out := cloneArgs(args)
	delete(out, "all")
	delete(out, "maxCount")
	out["offset"] = float64(0)

	tomorrow := today.AddDate(0, 0, 1)
	switch operation {
	case "overdue":
		delete(out, "startDateFrom")
		out["startDateTo"] = formatTaskDateTime(today)
	case "today":
		delete(out, "startDateFrom")
		out["startDateTo"] = formatTaskDateTime(tomorrow)
	case "only-today":
		out["startDateFrom"] = formatTaskDateTime(today)
		out["startDateTo"] = formatTaskDateTime(tomorrow)
	}
	return out
}

func isTaskInDateList(operation string, task map[string]any, today time.Time) bool {
	if !isActiveTask(task) {
		return false
	}
	start, ok := taskStartDate(task, today.Location())
	if !ok {
		return false
	}
	switch operation {
	case "overdue":
		return start.Before(today)
	case "today":
		return !start.After(today)
	case "only-today":
		return start.Equal(today)
	default:
		return false
	}
}

func isActiveTask(task map[string]any) bool {
	if removed, ok := boolArg(task["removed"]); ok && removed {
		return false
	}
	if checked, ok := numberArg(task["checked"]); ok && checked != 0 {
		return false
	}
	if complete, ok := numberArg(task["complete"]); ok && complete != 0 {
		return false
	}
	return true
}

func taskStartDate(task map[string]any, location *time.Location) (time.Time, bool) {
	start, ok := stringArg(task["start"])
	if !ok || start == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, start); err == nil {
		local := parsed.In(location)
		year, month, day := local.Date()
		return time.Date(year, month, day, 0, 0, 0, 0, location), true
	}
	parsed, err := time.ParseInLocation(taskDateLayout, start, location)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

const taskDateLayout = "2006-01-02"

func localDate(now time.Time) time.Time {
	year, month, day := now.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, now.Location())
}

func formatTaskDateTime(date time.Time) string {
	return date.Format(time.RFC3339)
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
	maps.Copy(normalized, body)

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

	var bodyData []byte
	if op.BodySchema != nil {
		bodyData, err = json.Marshal(args["body"])
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
	}

	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var body io.Reader
		if bodyData != nil {
			body = bytes.NewReader(bodyData)
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
		readLimit := c.maxResponseBytes
		if readLimit < int64(1<<63-1) {
			readLimit++
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, readLimit))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close response: %w", closeErr)
		}
		if int64(len(data)) > c.maxResponseBytes {
			return nil, &ClientError{Code: "response_too_large", Message: "Singularity API response exceeds configured limit", Metadata: map[string]any{"limit": c.maxResponseBytes}}
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if resp.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(data)) == 0 {
				return []byte(`{"ok":true}`), nil
			}
			if !json.Valid(data) {
				return nil, fmt.Errorf("invalid JSON response from %s %s", op.Method, op.Path)
			}
			return compactJSON(data), nil
		}

		retriable := op.Method == http.MethodGet && transientStatus(resp.StatusCode)
		retryAfter, hasRetryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), c.now())
		if !retriable || attempt == maxAttempts {
			apiErr := NewAPIError(resp.StatusCode, op.Method, op.Path, data, c.token)
			if retriable {
				apiErr.Retriable = true
				apiErr.Attempts = attempt
				if hasRetryAfter {
					seconds := int(retryAfter / time.Second)
					apiErr.RetryAfter = &seconds
				}
			}
			return nil, apiErr
		}
		delay := retryAfter
		if !hasRetryAfter {
			delay = fallbackBackoff(attempt, op.Path)
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
	panic("unreachable")
}

func transientStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		const maxDurationSeconds = int64((1<<63 - 1) / int64(time.Second))
		if seconds <= maxDurationSeconds {
			return time.Duration(seconds) * time.Second, true
		}
		return 0, false
	}
	date, err := http.ParseTime(value)
	if err != nil || date.Before(now) {
		return 0, false
	}
	return date.Sub(now), true
}

func fallbackBackoff(attempt int, path string) time.Duration {
	base := 200 * time.Millisecond * time.Duration(1<<(attempt-1))
	// Path/attempt-derived jitter is bounded and deterministic, avoiding both
	// synchronized clients and flaky tests.
	hash := sha256.Sum256([]byte(path + strconv.Itoa(attempt)))
	delay := base + time.Duration(hash[0]%21)*base/100
	return min(delay, 2*time.Second)
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
	if err := validateTaskDateFilters(op, args); err != nil {
		return err
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

func validateTaskDateFilters(op *Operation, args map[string]any) error {
	if op == nil || op.Tag != "task" || (op.Name != "list" && op.Name != "search") {
		return nil
	}
	for _, name := range []string{"startDateFrom", "startDateTo"} {
		value, ok := args[name]
		if !ok || value == nil {
			continue
		}
		raw, ok := stringArg(value)
		if !ok || raw == "" {
			return NewValidationError(name + " must be an RFC3339 timestamp")
		}
		if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
			return NewValidationError(name + " must be an RFC3339 timestamp")
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
	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		payload := map[string]any{"type": clientErr.Code, "message": clientErr.Message}
		maps.Copy(payload, clientErr.Metadata)
		return string(mustJSON(map[string]any{"error": payload}))
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		payload := map[string]any{
			"type":    "api_error",
			"status":  apiErr.Status,
			"method":  apiErr.Method,
			"path":    apiErr.Path,
			"message": "Singularity API request failed",
		}
		if apiErr.Retriable {
			payload["retriable"] = true
			payload["attempts"] = apiErr.Attempts
			if apiErr.RetryAfter != nil {
				payload["retryAfter"] = *apiErr.RetryAfter
			}
		}
		return string(mustJSON(map[string]any{"error": payload}))
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

func cloneArgsDeep(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for key, value := range args {
		switch typed := value.(type) {
		case map[string]any:
			out[key] = cloneArgsDeep(typed)
		case []any:
			items := make([]any, len(typed))
			copy(items, typed)
			out[key] = items
		default:
			out[key] = value
		}
	}
	return out
}

func cloneArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	maps.Copy(out, args)
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

func numberArg(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		n, err := strconv.ParseFloat(v.String(), 64)
		return n, err == nil
	default:
		return 0, false
	}
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
