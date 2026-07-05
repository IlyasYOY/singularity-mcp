package singularity

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const PageSize = 1000

var tagToTool = map[string]string{
	"project":        "singularity_projects",
	"taskGroup":      "singularity_task_groups",
	"task":           "singularity_tasks",
	"habit":          "singularity_habits",
	"habit-progress": "singularity_habit_progress",
	"checklist-item": "singularity_checklist_items",
	"tag":            "singularity_tags",
	"time-stat":      "singularity_time_stats",
}

var tagOrder = []string{
	"project",
	"taskGroup",
	"task",
	"habit",
	"habit-progress",
	"checklist-item",
	"tag",
	"time-stat",
}

var operationOrder = map[string]int{
	"list":        0,
	"inbox":       1,
	"overdue":     2,
	"today":       3,
	"only-today":  4,
	"search":      5,
	"get":         6,
	"create":      7,
	"update":      8,
	"delete":      9,
	"delete_bulk": 10,
}

type Catalog struct {
	TotalOperations int
	Groups          []*ToolGroup
	groupsByTool    map[string]*ToolGroup
	operationsByKey map[string]*Operation
	OmittedTags     []string
}

type ToolGroup struct {
	Tag        string
	ToolName   string
	Operations []*Operation
}

type Operation struct {
	Name              string
	Method            string
	Path              string
	OperationID       string
	Summary           string
	Tag               string
	QueryParams       []Parameter
	BodySchema        map[string]any
	BodyRequired      []string
	ListResponseField string
}

type Parameter struct {
	Name        string
	In          string
	Required    bool
	Description string
	Schema      map[string]any
}

type openAPIDoc struct {
	Paths      map[string]map[string]operationDoc `json:"paths"`
	Components struct {
		Schemas map[string]map[string]any `json:"schemas"`
	} `json:"components"`
}

type operationDoc struct {
	OperationID string                 `json:"operationId"`
	Summary     string                 `json:"summary"`
	Tags        []string               `json:"tags"`
	Parameters  []parameterDoc         `json:"parameters"`
	RequestBody *requestBodyDoc        `json:"requestBody"`
	Responses   map[string]responseDoc `json:"responses"`
}

type parameterDoc struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Required    bool           `json:"required"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
}

type requestBodyDoc struct {
	Required bool `json:"required"`
	Content  map[string]struct {
		Schema map[string]any `json:"schema"`
	} `json:"content"`
}

type responseDoc struct {
	Content map[string]struct {
		Schema map[string]any `json:"schema"`
	} `json:"content"`
}

func NewCatalog(snapshot []byte) (*Catalog, error) {
	var doc openAPIDoc
	if err := json.Unmarshal(snapshot, &doc); err != nil {
		return nil, fmt.Errorf("parse OpenAPI snapshot: %w", err)
	}
	if len(doc.Paths) == 0 {
		return nil, fmt.Errorf("OpenAPI snapshot has no paths")
	}

	catalog := &Catalog{
		groupsByTool:    make(map[string]*ToolGroup),
		operationsByKey: make(map[string]*Operation),
	}
	omitted := make(map[string]bool)

	paths := make([]string, 0, len(doc.Paths))
	for path := range doc.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		methods := doc.Paths[path]
		methodNames := make([]string, 0, len(methods))
		for method := range methods {
			methodNames = append(methodNames, method)
		}
		sort.Strings(methodNames)
		for _, method := range methodNames {
			opDoc := methods[method]
			catalog.TotalOperations++
			tag := exposedTag(opDoc.Tags)
			if tag == "" {
				for _, raw := range opDoc.Tags {
					if strings.Contains(raw, "kanban") {
						omitted[raw] = true
					}
				}
				continue
			}

			group := catalog.groupFor(tag)
			op, err := buildOperation(doc, tag, method, path, opDoc)
			if err != nil {
				return nil, err
			}
			group.Operations = append(group.Operations, op)
			catalog.operationsByKey[operationKey(group.ToolName, op.Name)] = op
		}
	}

	for tag := range omitted {
		catalog.OmittedTags = append(catalog.OmittedTags, tag)
	}
	sort.Strings(catalog.OmittedTags)
	catalog.addSyntheticOperations()
	catalog.addSyntheticSearchOperations()
	catalog.sortGroupsAndOperations()
	return catalog, nil
}

func (c *Catalog) Operation(toolName, operationName string) (*Operation, bool) {
	op, ok := c.operationsByKey[operationKey(toolName, operationName)]
	return op, ok
}

func (c *Catalog) Group(toolName string) (*ToolGroup, bool) {
	group, ok := c.groupsByTool[toolName]
	return group, ok
}

func (c *Catalog) ExposedOperationCount() int {
	count := 0
	for _, group := range c.Groups {
		count += len(group.Operations)
	}
	return count
}

func (c *Catalog) addSyntheticOperations() {
	group, ok := c.groupsByTool["singularity_tasks"]
	if !ok {
		return
	}
	listOp, ok := c.operationsByKey[operationKey("singularity_tasks", "list")]
	if !ok {
		return
	}
	specs := []struct {
		name        string
		operationID string
		summary     string
	}{
		{"inbox", "TaskControllerInbox", "Get inbox tasks"},
		{"overdue", "TaskControllerOverdue", "Get overdue active tasks"},
		{"today", "TaskControllerToday", "Get overdue and today's active tasks"},
		{"only-today", "TaskControllerOnlyToday", "Get today's active tasks"},
	}
	for _, spec := range specs {
		op := &Operation{
			Name:              spec.name,
			Method:            listOp.Method,
			Path:              listOp.Path,
			OperationID:       spec.operationID,
			Summary:           spec.summary,
			Tag:               listOp.Tag,
			QueryParams:       append([]Parameter(nil), listOp.QueryParams...),
			ListResponseField: listOp.ListResponseField,
		}
		group.Operations = append(group.Operations, op)
		c.operationsByKey[operationKey(group.ToolName, op.Name)] = op
	}
}

func (c *Catalog) addSyntheticSearchOperations() {
	specs := []struct {
		toolName    string
		operationID string
		summary     string
	}{
		{"singularity_tasks", "TaskControllerSearch", "Search tasks"},
		{"singularity_projects", "ProjectControllerSearch", "Search projects"},
		{"singularity_tags", "TagControllerSearch", "Search tags"},
	}
	for _, spec := range specs {
		group, ok := c.groupsByTool[spec.toolName]
		if !ok {
			continue
		}
		listOp, ok := c.operationsByKey[operationKey(spec.toolName, "list")]
		if !ok {
			continue
		}
		op := &Operation{
			Name:              "search",
			Method:            listOp.Method,
			Path:              listOp.Path,
			OperationID:       spec.operationID,
			Summary:           spec.summary,
			Tag:               listOp.Tag,
			QueryParams:       append([]Parameter(nil), listOp.QueryParams...),
			ListResponseField: listOp.ListResponseField,
		}
		group.Operations = append(group.Operations, op)
		c.operationsByKey[operationKey(group.ToolName, op.Name)] = op
	}
}

func (c *Catalog) sortGroupsAndOperations() {
	for _, group := range c.Groups {
		sort.Slice(group.Operations, func(i, j int) bool {
			left := operationOrder[group.Operations[i].Name]
			right := operationOrder[group.Operations[j].Name]
			if left == right {
				return group.Operations[i].OperationID < group.Operations[j].OperationID
			}
			return left < right
		})
	}
	sort.Slice(c.Groups, func(i, j int) bool {
		return tagIndex(c.Groups[i].Tag) < tagIndex(c.Groups[j].Tag)
	})
}

func (c *Catalog) groupFor(tag string) *ToolGroup {
	toolName := tagToTool[tag]
	if group := c.groupsByTool[toolName]; group != nil {
		return group
	}
	group := &ToolGroup{Tag: tag, ToolName: toolName}
	c.Groups = append(c.Groups, group)
	c.groupsByTool[toolName] = group
	return group
}

func buildOperation(doc openAPIDoc, tag, method, path string, opDoc operationDoc) (*Operation, error) {
	name := classifyOperation(method, path)
	if name == "" {
		return nil, fmt.Errorf("cannot classify %s %s", method, path)
	}
	op := &Operation{
		Name:        name,
		Method:      strings.ToUpper(method),
		Path:        path,
		OperationID: opDoc.OperationID,
		Summary:     opDoc.Summary,
		Tag:         tag,
	}
	for _, param := range opDoc.Parameters {
		if param.In != "query" {
			continue
		}
		op.QueryParams = append(op.QueryParams, Parameter{
			Name:        param.Name,
			In:          param.In,
			Required:    param.Required,
			Description: param.Description,
			Schema:      cloneMap(param.Schema),
		})
	}
	if opDoc.RequestBody != nil {
		schema, required, err := requestBodySchema(doc, opDoc.RequestBody)
		if err != nil {
			return nil, fmt.Errorf("%s request body: %w", opDoc.OperationID, err)
		}
		op.BodySchema = schema
		op.BodyRequired = required
	}
	if op.Name == "list" {
		op.ListResponseField = listResponseField(doc, opDoc.Responses)
	}
	return op, nil
}

func requestBodySchema(doc openAPIDoc, body *requestBodyDoc) (map[string]any, []string, error) {
	jsonContent, ok := body.Content["application/json"]
	if !ok {
		return nil, nil, fmt.Errorf("missing application/json content")
	}
	schema := cloneMap(jsonContent.Schema)
	if ref, _ := schema["$ref"].(string); ref != "" {
		resolved, ok := resolveSchema(doc, ref)
		if !ok {
			return nil, nil, fmt.Errorf("unknown schema ref %s", ref)
		}
		schema = resolved
	}
	required := stringSlice(schema["required"])
	return schema, required, nil
}

func listResponseField(doc openAPIDoc, responses map[string]responseDoc) string {
	resp, ok := responses["200"]
	if !ok {
		return ""
	}
	jsonContent, ok := resp.Content["application/json"]
	if !ok {
		return ""
	}
	schema := cloneMap(jsonContent.Schema)
	if ref, _ := schema["$ref"].(string); ref != "" {
		if resolved, ok := resolveSchema(doc, ref); ok {
			schema = resolved
		}
	}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		prop, _ := raw.(map[string]any)
		if typ, _ := prop["type"].(string); typ == "array" {
			return name
		}
	}
	return ""
}

func resolveSchema(doc openAPIDoc, ref string) (map[string]any, bool) {
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return nil, false
	}
	schema, ok := doc.Components.Schemas[strings.TrimPrefix(ref, prefix)]
	if !ok {
		return nil, false
	}
	return cloneMap(schema), true
}

func exposedTag(tags []string) string {
	for _, tag := range tags {
		if _, ok := tagToTool[tag]; ok {
			return tag
		}
	}
	return ""
}

func classifyOperation(method, path string) string {
	hasID := strings.Contains(path, "{id}")
	switch {
	case method == "get" && !hasID:
		return "list"
	case method == "post" && !hasID:
		return "create"
	case method == "delete" && !hasID:
		return "delete_bulk"
	case method == "get" && hasID:
		return "get"
	case method == "patch" && hasID:
		return "update"
	case method == "delete" && hasID:
		return "delete"
	default:
		return ""
	}
}

func tagIndex(tag string) int {
	for i, candidate := range tagOrder {
		if candidate == tag {
			return i
		}
	}
	return len(tagOrder)
}

func operationKey(toolName, operationName string) string {
	return toolName + "\x00" + operationName
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return value
	}
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
