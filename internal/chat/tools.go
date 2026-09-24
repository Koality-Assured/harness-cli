package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxToolRequestRounds = 8

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var schemaAnnotations = map[string]struct{}{
	"$id": {}, "$schema": {}, "default": {}, "deprecated": {}, "description": {},
	"examples": {}, "readOnly": {}, "title": {}, "writeOnly": {},
}
var schemaValidators = map[string]struct{}{
	"additionalProperties": {}, "const": {}, "enum": {}, "items": {}, "maximum": {},
	"maxLength": {}, "minimum": {}, "minLength": {}, "pattern": {}, "properties": {},
	"required": {}, "type": {},
}
var schemaTypes = map[string]struct{}{"array": {}, "boolean": {}, "integer": {}, "null": {}, "number": {}, "object": {}, "string": {}}

// ToolHandler is invoked synchronously after a provider response, like the Python registry.
type ToolHandler func(map[string]any) (any, error)

// ToolDispatcher exposes the subset the shared conversation loop needs.
type ToolDispatcher interface {
	ProviderDefinitions() []ToolDefinition
	ExecuteTool(string, any) ToolOutcome
}

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     ToolHandler
}

type ToolOutcome struct {
	Result any
	Error  string
}

// ToolRegistry contains only explicitly registered tools and defaults to empty.
type ToolRegistry struct {
	tools map[string]ToolDefinition
	order []string
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]ToolDefinition{}, order: []string{}}
}

func (r *ToolRegistry) Register(name, description string, schema map[string]any, handler ToolHandler) error {
	if !toolNamePattern.MatchString(name) {
		return errors.New("tool name must contain 1-64 letters, digits, underscores, or hyphens")
	}
	if r.tools == nil {
		r.tools = map[string]ToolDefinition{}
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q is already registered", name)
	}
	if handler == nil {
		return errors.New("tool handler must be callable")
	}
	if schema == nil {
		return errors.New("tool input schema must be a JSON object")
	}
	normalized, err := normalizeJSON(schema)
	if err != nil {
		return errors.New("tool input schema must contain JSON-compatible values")
	}
	copySchema, ok := normalized.(map[string]any)
	if !ok {
		return errors.New("tool input schema must be a JSON object")
	}
	if expected, exists := copySchema["type"]; exists && expected != "object" {
		return errors.New("tool input schema must describe an object")
	}
	if err := validateSchemaDefinition(copySchema, "$"); err != nil {
		return err
	}
	r.tools[name] = ToolDefinition{Name: name, Description: description, InputSchema: copySchema, Handler: handler}
	r.order = append(r.order, name)
	return nil
}

func (r *ToolRegistry) Definitions() []ToolDefinition {
	if r == nil {
		return nil
	}
	definitions := make([]ToolDefinition, 0, len(r.tools))
	for _, name := range r.order {
		tool, exists := r.tools[name]
		if !exists {
			continue
		}
		schemaValue, err := normalizeJSON(tool.InputSchema)
		if err != nil {
			continue
		}
		schema, _ := schemaValue.(map[string]any)
		definitions = append(definitions, ToolDefinition{Name: tool.Name, Description: tool.Description, InputSchema: schema, Handler: tool.Handler})
	}
	return definitions
}

func (r *ToolRegistry) Execute(name string, arguments any) ToolOutcome {
	tool, exists := r.tools[name]
	if !exists {
		return ToolOutcome{Error: fmt.Sprintf("Tool '%s' is not registered.", name)}
	}
	value, err := normalizeJSON(arguments)
	if err != nil {
		return ToolOutcome{Error: "Tool arguments must be JSON-compatible."}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return ToolOutcome{Error: "Tool arguments must be a JSON object."}
	}
	if err := validateValue(object, tool.InputSchema, "$"); err != nil {
		return ToolOutcome{Error: "Invalid arguments: " + err.Error()}
	}
	var result any
	var callErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				callErr = fmt.Errorf("tool handler panic: %v", recovered)
			}
		}()
		result, callErr = tool.Handler(object)
	}()
	if callErr != nil {
		return ToolOutcome{Error: fmt.Sprintf("Tool handler failed: %T: %v", callErr, callErr)}
	}
	if _, err := normalizeJSON(result); err != nil {
		return ToolOutcome{Error: "Tool result is not JSON serializable."}
	}
	return ToolOutcome{Result: result}
}

func providerToolDefinitions(registry *ToolRegistry) []ToolDefinition {
	if registry == nil {
		return nil
	}
	return registry.Definitions()
}

func (r *ToolRegistry) ProviderDefinitions() []ToolDefinition { return providerToolDefinitions(r) }

func (r *ToolRegistry) ExecuteTool(name string, arguments any) ToolOutcome {
	return r.Execute(name, arguments)
}

func normalizeJSON(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func validateSchemaDefinition(schema map[string]any, path string) error {
	for key := range schema {
		if _, ok := schemaAnnotations[key]; ok {
			continue
		}
		if _, ok := schemaValidators[key]; !ok {
			return fmt.Errorf("unsupported JSON Schema keyword %q at %s", key, path)
		}
	}
	if expected, exists := schema["type"]; exists {
		validType := func(value any) bool {
			text, ok := value.(string)
			if !ok {
				return false
			}
			_, found := schemaTypes[text]
			return found
		}
		switch types := expected.(type) {
		case string:
			if !validType(types) {
				return fmt.Errorf("invalid JSON Schema type at %s", path)
			}
		case []any:
			if len(types) == 0 {
				return fmt.Errorf("invalid JSON Schema type at %s", path)
			}
			for _, kind := range types {
				if !validType(kind) {
					return fmt.Errorf("invalid JSON Schema type at %s", path)
				}
			}
		default:
			return fmt.Errorf("invalid JSON Schema type at %s", path)
		}
	}
	properties, exists := schema["properties"]
	if exists {
		object, ok := properties.(map[string]any)
		if !ok {
			return fmt.Errorf("JSON Schema properties must be an object at %s", path)
		}
		for name, childValue := range object {
			child, ok := childValue.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid JSON Schema property at %s", path)
			}
			if err := validateSchemaDefinition(child, path+"."+name); err != nil {
				return err
			}
		}
	}
	if required, exists := schema["required"]; exists {
		values, ok := required.([]any)
		if !ok {
			return fmt.Errorf("JSON Schema required must be a list of strings at %s", path)
		}
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("JSON Schema required must be a list of strings at %s", path)
			}
		}
	}
	if additional, exists := schema["additionalProperties"]; exists {
		switch value := additional.(type) {
		case bool:
		case map[string]any:
			if err := validateSchemaDefinition(value, path+".*"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("JSON Schema additionalProperties must be boolean or schema at %s", path)
		}
	}
	if items, exists := schema["items"]; exists {
		itemSchema, ok := items.(map[string]any)
		if !ok {
			return fmt.Errorf("JSON Schema items must be a schema object at %s", path)
		}
		if err := validateSchemaDefinition(itemSchema, path+"[]"); err != nil {
			return err
		}
	}
	if enum, exists := schema["enum"]; exists {
		if _, ok := enum.([]any); !ok {
			return fmt.Errorf("JSON Schema enum must be an array at %s", path)
		}
	}
	for _, keyword := range []string{"minimum", "maximum"} {
		if value, exists := schema[keyword]; exists {
			if !isJSONNumber(value) {
				return fmt.Errorf("JSON Schema %s must be numeric at %s", keyword, path)
			}
		}
	}
	for _, keyword := range []string{"minLength", "maxLength"} {
		if value, exists := schema[keyword]; exists {
			number, ok := value.(json.Number)
			if !ok {
				return fmt.Errorf("JSON Schema %s must be a non-negative integer at %s", keyword, path)
			}
			parsed, err := number.Int64()
			if err != nil || parsed < 0 {
				return fmt.Errorf("JSON Schema %s must be a non-negative integer at %s", keyword, path)
			}
		}
	}
	if pattern, exists := schema["pattern"]; exists {
		text, ok := pattern.(string)
		if !ok {
			return fmt.Errorf("JSON Schema pattern must be a string at %s", path)
		}
		if _, err := regexp.Compile(text); err != nil {
			return fmt.Errorf("invalid JSON Schema pattern at %s", path)
		}
	}
	return nil
}

func isJSONNumber(value any) bool { _, ok := value.(json.Number); return ok }

func validateValue(value any, schema map[string]any, path string) error {
	if expected, exists := schema["type"]; exists {
		matches := false
		switch types := expected.(type) {
		case string:
			matches = matchesType(value, types)
		case []any:
			for _, kind := range types {
				if expectedType, ok := kind.(string); ok && matchesType(value, expectedType) {
					matches = true
					break
				}
			}
		}
		if !matches {
			if typeName, ok := expected.(string); ok {
				return fmt.Errorf("%s must be %s", path, typeName)
			}
			return fmt.Errorf("%s has the wrong type", path)
		}
	}
	if constant, exists := schema["const"]; exists && !jsonValuesEqual(value, constant) {
		return fmt.Errorf("%s does not match the required value", path)
	}
	if enumValue, exists := schema["enum"]; exists {
		allowed, _ := enumValue.([]any)
		found := false
		for _, item := range allowed {
			if jsonValuesEqual(value, item) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s is not an allowed value", path)
		}
	}
	switch current := value.(type) {
	case map[string]any:
		required, _ := schema["required"].([]any)
		for _, rawKey := range required {
			if key, ok := rawKey.(string); ok {
				if _, present := current[key]; !present {
					return fmt.Errorf("%s.%s is required", path, key)
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		additional := schema["additionalProperties"]
		for key, child := range current {
			if rawSchema, exists := properties[key]; exists {
				if childSchema, ok := rawSchema.(map[string]any); ok {
					if err := validateValue(child, childSchema, path+"."+key); err != nil {
						return err
					}
				}
				continue
			}
			switch extra := additional.(type) {
			case bool:
				if !extra {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
			case map[string]any:
				if err := validateValue(child, extra, path+"."+key); err != nil {
					return err
				}
			}
		}
	case []any:
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for index, child := range current {
				if err := validateValue(child, itemSchema, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
	case string:
		length := utf8.RuneCountInString(current)
		if limit, ok := schemaInt(schema["minLength"]); ok && length < limit {
			return fmt.Errorf("%s is shorter than allowed", path)
		}
		if limit, ok := schemaInt(schema["maxLength"]); ok && length > limit {
			return fmt.Errorf("%s is longer than allowed", path)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			matched, err := regexp.MatchString(pattern, current)
			if err != nil || !matched {
				return fmt.Errorf("%s does not match the required pattern", path)
			}
		}
	}
	if isJSONNumber(value) {
		if bound, ok := schema["minimum"]; ok && compareJSONNumbers(value, bound) < 0 {
			return fmt.Errorf("%s is below the allowed minimum", path)
		}
		if bound, ok := schema["maximum"]; ok && compareJSONNumbers(value, bound) > 0 {
			return fmt.Errorf("%s is above the allowed maximum", path)
		}
	}
	return nil
}

func matchesType(value any, expected string) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		text := string(number)
		return !strings.ContainsAny(text, ".eE")
	case "number":
		return isJSONNumber(value)
	case "null":
		return value == nil
	default:
		return false
	}
}

func schemaInt(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	if err != nil || int64(int(parsed)) != parsed {
		return 0, false
	}
	return int(parsed), true
}

func compareJSONNumbers(a, b any) int {
	left, _ := new(big.Rat).SetString(fmt.Sprint(a))
	right, _ := new(big.Rat).SetString(fmt.Sprint(b))
	if left == nil || right == nil {
		return 0
	}
	return left.Cmp(right)
}

func jsonValuesEqual(a, b any) bool {
	if isJSONNumber(a) && isJSONNumber(b) {
		return compareJSONNumbers(a, b) == 0
	}
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, exists := right[key]
			if !exists || !jsonValuesEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := b.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !jsonValuesEqual(left[i], right[i]) {
				return false
			}
		}
		return true
	default:
		return fmt.Sprintf("%T:%v", a, a) == fmt.Sprintf("%T:%v", b, b)
	}
}

func parseToolArguments(value any) (map[string]any, error) {
	if raw, ok := value.(string); ok {
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		object, ok := decoded.(map[string]any)
		if !ok {
			return nil, errors.New("tool arguments must be a JSON object")
		}
		return object, nil
	}
	decoded, err := normalizeJSON(value)
	if err != nil {
		return nil, err
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("tool arguments must be a JSON object")
	}
	return object, nil
}

func validateToolArguments(value any, schema map[string]any) error {
	arguments, err := parseToolArguments(value)
	if err != nil {
		return err
	}
	return validateValue(arguments, schema, "$")
}

func parseJSONNumber(text string) (json.Number, error) {
	if _, err := strconv.ParseFloat(text, 64); err != nil {
		return "", err
	}
	return json.Number(text), nil
}
