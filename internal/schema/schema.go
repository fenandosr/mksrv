// SPDX-License-Identifier: Apache-2.0

// Package schema validates JSON-compatible workspace values against the
// embedded draft 2020-12 schemas using santhosh-tekuri/jsonschema/v6. YAML
// documents are converted to JSON with sigs.k8s.io/yaml before validation.
package schema

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"

	engineassets "github.com/fenandosr/mksrv"
)

// Issue is one schema-validation finding.
type Issue struct {
	Path    string `json:"path"`
	Keyword string `json:"keyword"`
	Message string `json:"message"`
}

// Validator compiles and caches embedded schemas.
type Validator struct {
	mu       sync.Mutex
	compiled map[string]*jsonschema.Schema
}

// New creates a Validator with an empty compile cache.
func New() *Validator { return &Validator{compiled: make(map[string]*jsonschema.Schema)} }

// ValidateYAML parses a YAML document and validates it against an embedded schema.
// The returned value is the JSON-compatible decoding of the document.
func (v *Validator) ValidateYAML(schemaName string, data []byte) (any, []Issue, error) {
	jsonBytes, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, nil, fmt.Errorf("parse YAML: %w", err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("decode document: %w", err)
	}
	issues, err := v.Validate(schemaName, value)
	return value, issues, err
}

// Validate validates a JSON-compatible value against an embedded schema.
func (v *Validator) Validate(schemaName string, value any) ([]Issue, error) {
	compiled, err := v.load(schemaName)
	if err != nil {
		return nil, err
	}
	err = compiled.Validate(value)
	if err == nil {
		return []Issue{}, nil
	}
	var validationErr *jsonschema.ValidationError
	if !errors.As(err, &validationErr) {
		return nil, err
	}
	issues := flatten(validationErr)
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Keyword < issues[j].Keyword
		}
		return issues[i].Path < issues[j].Path
	})
	return issues, nil
}

func (v *Validator) load(schemaName string) (*jsonschema.Schema, error) {
	name := path.Base(schemaName)
	v.mu.Lock()
	defer v.mu.Unlock()
	if cached := v.compiled[name]; cached != nil {
		return cached, nil
	}
	data, err := engineassets.FS.ReadFile("schemas/" + name)
	if err != nil {
		return nil, fmt.Errorf("read embedded schema %q: %w", name, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse embedded schema %q: %w", name, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	location := "mem:///" + name
	if err := compiler.AddResource(location, document); err != nil {
		return nil, fmt.Errorf("register embedded schema %q: %w", name, err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("compile embedded schema %q: %w", name, err)
	}
	v.compiled[name] = compiled
	return compiled, nil
}

func flatten(root *jsonschema.ValidationError) []Issue {
	basic := root.BasicOutput()
	issues := make([]Issue, 0, len(basic.Errors)+1)
	for _, unit := range basic.Errors {
		if unit.Error == nil {
			continue
		}
		issues = append(issues, Issue{
			Path:    pointerToPath(unit.InstanceLocation),
			Keyword: keywordOf(unit.KeywordLocation),
			Message: unit.Error.String(),
		})
	}
	if len(issues) == 0 {
		message := root.Error()
		if basic.Error != nil {
			message = basic.Error.String()
		}
		issues = append(issues, Issue{
			Path:    pointerToPath(basic.InstanceLocation),
			Keyword: keywordOf(basic.KeywordLocation),
			Message: message,
		})
	}
	return issues
}

// pointerToPath rewrites an RFC 6901 JSON Pointer as a "$"-rooted path such as
// "$.hosts.edge.stacks[0]".
func pointerToPath(pointer string) string {
	pointer = strings.TrimSpace(pointer)
	if pointer == "" || pointer == "/" {
		return "$"
	}
	var builder strings.Builder
	builder.WriteString("$")
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(token, "~1", "/")
		token = strings.ReplaceAll(token, "~0", "~")
		switch {
		case isArrayIndex(token):
			builder.WriteString("[" + token + "]")
		case isIdentifier(token):
			builder.WriteString("." + token)
		default:
			builder.WriteString("[" + strconv.Quote(token) + "]")
		}
	}
	return builder.String()
}

func keywordOf(keywordLocation string) string {
	keywordLocation = strings.Trim(strings.TrimSpace(keywordLocation), "/")
	if keywordLocation == "" {
		return ""
	}
	parts := strings.Split(keywordLocation, "/")
	return parts[len(parts)-1]
}

func isArrayIndex(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isIdentifier(token string) bool {
	if token == "" {
		return false
	}
	for index, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case index > 0 && (r == '-' || (r >= '0' && r <= '9')):
		default:
			return false
		}
	}
	return true
}
