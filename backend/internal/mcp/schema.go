// Argument schemas: one place builds them from a tool's parameter list, and the same place
// validates a call against them. A tool therefore cannot drift from its own schema, and a
// caller's arguments are checked before any capability runs.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Kind is the JSON type of one argument.
type Kind string

const (
	KindString  Kind = "string"
	KindInteger Kind = "integer"
	KindNumber  Kind = "number"
	KindBoolean Kind = "boolean"
	KindObject  Kind = "object"
	KindArray   Kind = "array"
)

// Property declares one argument: its type, whether it is required, what it means, and — for
// arrays — the type of its elements. Enum narrows a string to a fixed set.
type Property struct {
	Name        string
	Kind        Kind
	Items       Kind // element type of an array ("" ⇒ unconstrained)
	Enum        []string
	Required    bool
	Description string
}

// ObjectSchema renders a parameter list as a JSON Schema object. Unknown arguments are refused
// (additionalProperties: false) so a misspelled argument is named instead of silently dropped.
func ObjectSchema(props []Property) json.RawMessage {
	doc := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}
	fields := map[string]any{}
	var required []string
	for _, p := range props {
		f := map[string]any{"type": string(p.Kind)}
		if p.Description != "" {
			f["description"] = p.Description
		}
		if p.Kind == KindArray && p.Items != "" {
			f["items"] = map[string]any{"type": string(p.Items)}
		}
		if len(p.Enum) > 0 {
			f["enum"] = p.Enum
		}
		fields[p.Name] = f
		if p.Required {
			required = append(required, p.Name)
		}
	}
	doc["properties"] = fields
	if len(required) > 0 {
		sort.Strings(required)
		doc["required"] = required
	}
	raw, err := json.Marshal(doc)
	if err != nil { // unreachable: every value above is JSON-encodable
		return json.RawMessage(`{"type":"object"}`)
	}
	return raw
}

// schemaDoc is the subset of JSON Schema the tool table uses — parsed back from the schema so
// validation reads the same document a client received.
type schemaDoc struct {
	Type                 string                `json:"type"`
	Properties           map[string]schemaProp `json:"properties"`
	Required             []string              `json:"required"`
	AdditionalProperties *bool                 `json:"additionalProperties"`
}

type schemaProp struct {
	Type  string      `json:"type"`
	Items *schemaProp `json:"items"`
	Enum  []string    `json:"enum"`
}

// ValidateArgs checks a tools/call argument object against a tool's schema: required arguments
// present, no unknown arguments, every value of the declared type. The message names the
// argument at fault, so a caller can fix the call without guessing.
func ValidateArgs(schema, args json.RawMessage) error {
	var doc schemaDoc
	if len(schema) > 0 {
		if err := json.Unmarshal(schema, &doc); err != nil {
			return errors.New("the tool's argument schema is unreadable")
		}
	}
	given := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(string(args)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(args, &given); err != nil {
			return errors.New("arguments must be a JSON object")
		}
	}
	// What was GIVEN is judged first: a misspelled or mistyped argument is named as itself,
	// instead of being reported as the required argument it failed to be.
	strict := doc.AdditionalProperties == nil || !*doc.AdditionalProperties
	names := make([]string, 0, len(given))
	for name := range given {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic first complaint
	for _, name := range names {
		raw := given[name]
		prop, known := doc.Properties[name]
		if !known {
			if strict {
				return fmt.Errorf("unknown argument %q", name)
			}
			continue
		}
		if isNull(raw) { // an explicit null clears an optional argument
			continue
		}
		if err := checkValue(name, prop, raw); err != nil {
			return err
		}
	}
	for _, name := range doc.Required {
		raw, ok := given[name]
		if !ok || isNull(raw) {
			return fmt.Errorf("argument %q is required", name)
		}
	}
	return nil
}

func checkValue(name string, prop schemaProp, raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("argument %q is not valid JSON", name)
	}
	if prop.Type != "" && !matches(Kind(prop.Type), v) {
		return fmt.Errorf("argument %q must be of type %s", name, prop.Type)
	}
	if len(prop.Enum) > 0 {
		s, _ := v.(string)
		if !contains(prop.Enum, s) {
			return fmt.Errorf("argument %q must be one of: %s", name, strings.Join(prop.Enum, ", "))
		}
	}
	if Kind(prop.Type) == KindArray && prop.Items != nil && prop.Items.Type != "" {
		items, _ := v.([]any)
		for i, item := range items {
			if !matches(Kind(prop.Items.Type), item) {
				return fmt.Errorf("argument %q[%d] must be of type %s", name, i, prop.Items.Type)
			}
		}
	}
	return nil
}

func matches(kind Kind, v any) bool {
	switch kind {
	case KindString:
		_, ok := v.(string)
		return ok
	case KindBoolean:
		_, ok := v.(bool)
		return ok
	case KindNumber:
		_, ok := v.(float64)
		return ok
	case KindInteger:
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case KindObject:
		_, ok := v.(map[string]any)
		return ok
	case KindArray:
		_, ok := v.([]any)
		return ok
	default:
		return true
	}
}

func isNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
