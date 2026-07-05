package singularity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 100
)

type searchOptions struct {
	Query      string
	Fields     []string
	Limit      int
	Compact    bool
	AllPages   bool
	TagIDs     []string
	TagMode    string
	Checked    *float64
	Priority   *float64
	IsNotebook *bool
	Parent     *string
}

func (c *APIClient) search(ctx context.Context, op *Operation, args map[string]any) ([]byte, error) {
	options, err := parseSearchOptions(op, args)
	if err != nil {
		return nil, err
	}
	listArgs := searchListArgs(op, args)

	var raw []byte
	if options.AllPages {
		raw, err = c.callAllPages(ctx, op, listArgs)
	} else {
		raw, err = c.callOnce(ctx, op, listArgs, nil)
	}
	if err != nil {
		return nil, err
	}

	var page map[string]any
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	listField := op.ListResponseField
	if listField == "" {
		listField = firstArrayField(page)
	}
	items, _ := page[listField].([]any)

	results := make([]any, 0, options.Limit)
	truncated := false
	for _, item := range items {
		entity, ok := item.(map[string]any)
		if !ok || !matchesSearch(op, options, entity) {
			continue
		}
		if len(results) >= options.Limit {
			truncated = true
			break
		}
		if options.Compact {
			results = append(results, compactSearchEntity(op.Tag, entity))
		} else {
			results = append(results, entity)
		}
	}

	return marshalJSON(map[string]any{
		"count":   len(results),
		listField: results,
		"query": map[string]any{
			"text":      options.Query,
			"fields":    options.Fields,
			"limit":     options.Limit,
			"truncated": truncated,
		},
	})
}

func parseSearchOptions(op *Operation, args map[string]any) (searchOptions, error) {
	options := searchOptions{
		Query:    strings.TrimSpace(stringValue(args["query"])),
		Fields:   []string{"title"},
		Limit:    defaultSearchLimit,
		Compact:  true,
		AllPages: true,
		TagMode:  "any",
	}
	if value, ok := args["fields"]; ok && value != nil {
		fields, err := stringList(value, "fields")
		if err != nil {
			return options, err
		}
		if len(fields) == 0 {
			return options, NewValidationError("fields must not be empty")
		}
		options.Fields = fields
	}
	if value, ok := args["limit"]; ok && value != nil {
		limit := intArg(value, 0)
		if limit < 1 || limit > maxSearchLimit {
			return options, NewValidationError("limit must be between 1 and 100")
		}
		options.Limit = limit
	}
	if compact, ok := boolArg(args["compact"]); ok {
		options.Compact = compact
	}
	if allPages, ok := boolArg(args["all"]); ok {
		options.AllPages = allPages
	}
	if tag, ok := stringArg(args["tag"]); ok && tag != "" {
		options.TagIDs = append(options.TagIDs, tag)
	}
	if value, ok := args["tags"]; ok && value != nil {
		tags, err := stringList(value, "tags")
		if err != nil {
			return options, err
		}
		options.TagIDs = append(options.TagIDs, tags...)
	}
	if tagMode, ok := stringArg(args["tagMode"]); ok && tagMode != "" {
		if tagMode != "any" && tagMode != "all" {
			return options, NewValidationError("tagMode must be any or all")
		}
		options.TagMode = tagMode
	}
	if checked, ok := numberArg(args["checked"]); ok {
		options.Checked = &checked
	}
	if priority, ok := numberArg(args["priority"]); ok {
		options.Priority = &priority
	}
	if isNotebook, ok := boolArg(args["isNotebook"]); ok {
		options.IsNotebook = &isNotebook
	}
	if parent, ok := stringArg(args["parent"]); ok && parent != "" {
		options.Parent = &parent
	}
	if err := validateSearchFields(op, options.Fields); err != nil {
		return options, err
	}
	if len(options.TagIDs) > 0 && op.Tag != "task" {
		return options, NewValidationError("tag and tags filters are only supported for task search")
	}
	if !hasSearchCriteria(op, args, options) {
		return options, NewValidationError("query or at least one search filter is required")
	}
	return options, nil
}

func validateSearchFields(op *Operation, fields []string) error {
	allowed := map[string]bool{"id": true, "title": true}
	if op.Tag == "task" || op.Tag == "project" {
		allowed["note"] = true
	}
	for _, field := range fields {
		if !allowed[field] {
			return NewValidationError("unsupported search field: " + field)
		}
	}
	return nil
}

func hasSearchCriteria(op *Operation, args map[string]any, options searchOptions) bool {
	if options.Query != "" || len(options.TagIDs) > 0 || options.Checked != nil || options.Priority != nil || options.IsNotebook != nil || options.Parent != nil {
		return true
	}
	for _, param := range op.QueryParams {
		if value, ok := args[param.Name]; ok && value != nil && formatQueryValue(value) != "" {
			return true
		}
	}
	return false
}

func searchListArgs(op *Operation, args map[string]any) map[string]any {
	out := map[string]any{}
	for _, param := range op.QueryParams {
		if value, ok := args[param.Name]; ok {
			out[param.Name] = value
		}
	}
	if value, ok := args["all"]; ok {
		out["all"] = value
	}
	return out
}

func matchesSearch(op *Operation, options searchOptions, item map[string]any) bool {
	if options.Query != "" && !matchesSearchText(item, options.Query, options.Fields) {
		return false
	}
	if len(options.TagIDs) > 0 && !matchesTags(item, options.TagIDs, options.TagMode) {
		return false
	}
	if options.Checked != nil && !numberEquals(item["checked"], *options.Checked) {
		return false
	}
	if options.Priority != nil && !numberEquals(item["priority"], *options.Priority) {
		return false
	}
	if options.IsNotebook != nil {
		value, ok := boolArg(item["isNotebook"])
		if !ok || value != *options.IsNotebook {
			return false
		}
	}
	if options.Parent != nil {
		parent, _ := stringArg(item["parent"])
		if parent != *options.Parent {
			return false
		}
	}
	return true
}

func matchesSearchText(item map[string]any, query string, fields []string) bool {
	needle := strings.ToLower(query)
	for _, field := range fields {
		value := strings.ToLower(stringValue(item[field]))
		if value != "" && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func matchesTags(item map[string]any, want []string, mode string) bool {
	actual := map[string]bool{}
	switch tags := item["tags"].(type) {
	case []any:
		for _, tag := range tags {
			if s, ok := tag.(string); ok {
				actual[s] = true
			}
		}
	case []string:
		for _, tag := range tags {
			actual[tag] = true
		}
	}
	if mode == "all" {
		for _, tag := range want {
			if !actual[tag] {
				return false
			}
		}
		return true
	}
	for _, tag := range want {
		if actual[tag] {
			return true
		}
	}
	return false
}

func compactSearchEntity(tag string, item map[string]any) map[string]any {
	if tag == "task" {
		return compactEntity(item)
	}
	keep := []string{
		"id",
		"title",
		"parent",
		"projectId",
		"tags",
		"start",
		"deadline",
		"priority",
		"state",
		"checked",
		"complete",
		"isNotebook",
		"color",
		"emoji",
		"createdDate",
		"modificatedDate",
	}
	out := make(map[string]any, len(keep))
	for _, key := range keep {
		if value, ok := item[key]; ok {
			out[key] = value
		}
	}
	return out
}

func stringList(value any, name string) ([]string, error) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return nil, NewValidationError(name + " must contain only strings")
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, NewValidationError(name + " must be an array of strings")
	}
}

func stringValue(value any) string {
	s, _ := stringArg(value)
	return s
}

func numberEquals(value any, want float64) bool {
	got, ok := numberArg(value)
	return ok && got == want
}
