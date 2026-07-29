package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// A parameter list renders as a strict JSON Schema: declared types, sorted required list, closed
// against unknown arguments (a misspelling is named, never silently dropped).
func TestObjectSchemaRendersTheDeclaration(t *testing.T) {
	raw := ObjectSchema([]Property{
		{Name: "path", Kind: KindString, Required: true, Description: "Where."},
		{Name: "days", Kind: KindInteger, Description: "How far."},
		{Name: "kind", Kind: KindString, Enum: []string{"todo", "auto"}},
		{Name: "order", Kind: KindArray, Items: KindString, Required: true},
	})
	var doc struct {
		Type                 string                     `json:"type"`
		AdditionalProperties bool                       `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("schema is not JSON: %v: %s", err, raw)
	}
	if doc.Type != "object" || doc.AdditionalProperties {
		t.Errorf("an argument object closed against unknown keys: %s", raw)
	}
	if len(doc.Required) != 2 || doc.Required[0] != "order" || doc.Required[1] != "path" {
		t.Errorf("required arguments are listed deterministically: %v", doc.Required)
	}
	if len(doc.Properties) != 4 {
		t.Errorf("every declared argument appears: %s", raw)
	}
	if !strings.Contains(string(doc.Properties["path"]), `"description":"Where."`) {
		t.Errorf("an argument keeps its description: %s", doc.Properties["path"])
	}
	if !strings.Contains(string(doc.Properties["order"]), `"items":{"type":"string"}`) {
		t.Errorf("a list declares its element type: %s", doc.Properties["order"])
	}
	if !strings.Contains(string(doc.Properties["kind"]), `"enum":["todo","auto"]`) {
		t.Errorf("a narrowed argument declares its values: %s", doc.Properties["kind"])
	}
}

// Validation names the argument at fault — a caller can fix the call without guessing.
func TestValidateArgs(t *testing.T) {
	schema := ObjectSchema([]Property{
		{Name: "id", Kind: KindString, Required: true},
		{Name: "days", Kind: KindInteger},
		{Name: "ratio", Kind: KindNumber},
		{Name: "fresh", Kind: KindBoolean},
		{Name: "kind", Kind: KindString, Enum: []string{"todo", "auto"}},
		{Name: "order", Kind: KindArray, Items: KindString},
		{Name: "tuning", Kind: KindObject},
	})
	cases := []struct {
		name string
		args string
		want string // "" ⇒ accepted
	}{
		{"complete", `{"id":"r1","days":7,"ratio":0.5,"fresh":true,"kind":"auto","order":["a"],"tuning":{"x":1}}`, ""},
		{"only required", `{"id":"r1"}`, ""},
		{"no arguments at all", ``, `"id" is required`},
		{"null arguments", `null`, `"id" is required`},
		{"missing required", `{"days":7}`, `"id" is required`},
		{"explicit null for required", `{"id":null}`, `"id" is required`},
		{"explicit null clears optional", `{"id":"r1","days":null}`, ""},
		{"unknown argument", `{"id":"r1","dayz":7}`, `unknown argument "dayz"`},
		{"misspelled required argument names itself", `{"idd":"r1"}`, `unknown argument "idd"`},
		{"wrong type", `{"id":7}`, `"id" must be of type string`},
		{"fractional integer", `{"id":"r1","days":1.5}`, `"days" must be of type integer`},
		{"number takes an integer", `{"id":"r1","ratio":2}`, ""},
		{"enum violation", `{"id":"r1","kind":"whatever"}`, `"kind" must be one of: todo, auto`},
		{"list element type", `{"id":"r1","order":["a",2]}`, `"order"[1] must be of type string`},
		{"object expected", `{"id":"r1","tuning":"x"}`, `"tuning" must be of type object`},
		{"arguments not an object", `[1,2]`, "arguments must be a JSON object"},
	}
	for _, c := range cases {
		err := ValidateArgs(schema, json.RawMessage(c.args))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: accepted, got %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
}

// A tool without arguments accepts an absent, empty or null argument object.
func TestValidateArgsWithoutParameters(t *testing.T) {
	schema := ObjectSchema(nil)
	for _, args := range []string{``, `null`, `{}`} {
		if err := ValidateArgs(schema, json.RawMessage(args)); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
	if err := ValidateArgs(schema, json.RawMessage(`{"stray":1}`)); err == nil {
		t.Error("a tool without arguments still refuses a stray one")
	}
}
