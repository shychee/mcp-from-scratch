package mcpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

const (
	maxToolSchemaBytes   = 1 << 20
	maxToolArgumentBytes = 1 << 20
	maxJSONNestingDepth  = 64
	maxToolSchemaNesting = 64
)

func prepareTool(tool Tool) (registeredTool, error) {
	if tool == nil || isNilTool(tool) {
		return registeredTool{}, fmt.Errorf("register tool: nil tool")
	}
	definition, err := cloneToolDefinition(tool.Definition())
	if err != nil {
		return registeredTool{}, fmt.Errorf("register tool: clone definition: %w", err)
	}
	if strings.TrimSpace(definition.Name) == "" {
		return registeredTool{}, fmt.Errorf("register tool: missing tool name")
	}
	if definition.InputSchema == nil {
		return registeredTool{}, fmt.Errorf("register tool %q: missing input schema", definition.Name)
	}
	inputSchema, err := compileSchema(definition.InputSchema, true)
	if err != nil {
		return registeredTool{}, fmt.Errorf("register tool %q: invalid input schema: %w", definition.Name, err)
	}
	var outputSchema *jsonschema.Resolved
	if definition.OutputSchema != nil {
		outputSchema, err = compileSchema(definition.OutputSchema, false)
		if err != nil {
			return registeredTool{}, fmt.Errorf("register tool %q: invalid output schema: %w", definition.Name, err)
		}
	}
	return registeredTool{
		tool:         tool,
		definition:   definition,
		inputSchema:  inputSchema,
		outputSchema: outputSchema,
	}, nil
}

func cloneToolDefinition(definition ToolDefinition) (ToolDefinition, error) {
	encoded, err := json.Marshal(definition)
	if err != nil {
		return ToolDefinition{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var cloned ToolDefinition
	if err := decoder.Decode(&cloned); err != nil {
		return ToolDefinition{}, err
	}
	return cloned, nil
}

func isNilTool(tool Tool) bool {
	value := reflect.ValueOf(tool)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func compileSchema(raw map[string]any, input bool) (*jsonschema.Resolved, error) {
	if input {
		if schemaType, _ := raw["type"].(string); schemaType != "object" {
			return nil, fmt.Errorf("input schema top-level type must be object")
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode schema: %w", err)
	}
	if len(encoded) > maxToolSchemaBytes {
		return nil, fmt.Errorf("schema exceeds %d bytes", maxToolSchemaBytes)
	}
	if err := validateJSONDepth(encoded, maxToolSchemaNesting); err != nil {
		return nil, err
	}
	if err := rejectExternalReferences(raw); err != nil {
		return nil, err
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve schema: %w", err)
	}
	return resolved, nil
}

func validateToolArguments(schema *jsonschema.Resolved, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return fmt.Errorf("tool arguments must be an object")
	}
	if len(raw) > maxToolArgumentBytes {
		return fmt.Errorf("tool arguments exceed %d bytes", maxToolArgumentBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var rawValue any
	if err := decoder.Decode(&rawValue); err != nil {
		return fmt.Errorf("decode tool arguments")
	}
	rawValue = normalizeJSONNumbers(rawValue)
	arguments, ok := rawValue.(map[string]any)
	if !ok {
		return fmt.Errorf("tool arguments must be an object")
	}
	if err := validateJSONDepth(raw, maxJSONNestingDepth); err != nil {
		return err
	}
	if err := schema.Validate(arguments); err != nil {
		return fmt.Errorf("tool arguments do not match input schema")
	}
	return nil
}

func normalizeJSONNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed
	case map[string]any:
		for key, child := range typed {
			typed[key] = normalizeJSONNumbers(child)
		}
	case []any:
		for index, child := range typed {
			typed[index] = normalizeJSONNumbers(child)
		}
	}
	return value
}

func validateJSONDepth(raw []byte, maxDepth int) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("invalid JSON: %w", err)
		}
		switch token {
		case json.Delim('['), json.Delim('{'):
			depth++
			if depth > maxDepth {
				return fmt.Errorf("JSON nesting exceeds %d levels", maxDepth)
			}
		case json.Delim(']'), json.Delim('}'):
			depth--
		}
	}
}

func rejectExternalReferences(value any) error {
	var walk func(any) error
	walk = func(current any) error {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if (key == "$ref" || key == "$dynamicRef") && child != nil {
					ref, ok := child.(string)
					if !ok || !strings.HasPrefix(ref, "#") {
						return fmt.Errorf("external schema reference %q is not allowed", child)
					}
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}
