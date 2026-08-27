// SPDX-License-Identifier: Apache-2.0

// Package schema validates JSON-compatible workspace values against the
// embedded draft 2020-12 schemas. M0 implements the vocabulary used by the
// checked-in schemas; ADR 0006 records the planned validator replacement.
package schema

import (
	"encoding/json"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	engineassets "github.com/fenandosr/mksrv"
	"github.com/fenandosr/mksrv/internal/yamlmini"
)

// Issue is one schema-validation finding.
type Issue struct {
	Path    string `json:"path"`
	Keyword string `json:"keyword"`
	Message string `json:"message"`
}

// Validator loads and caches embedded schemas.
type Validator struct {
	mu      sync.RWMutex
	schemas map[string]map[string]any
}

func New() *Validator { return &Validator{schemas: make(map[string]map[string]any)} }

// ValidateYAML parses YAML and validates it against an embedded schema.
func (v *Validator) ValidateYAML(schemaName string, data []byte) (any, []Issue, error) {
	value, err := yamlmini.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	issues, err := v.Validate(schemaName, value)
	return value, issues, err
}

// Validate validates a JSON-compatible value against an embedded schema.
func (v *Validator) Validate(schemaName string, value any) ([]Issue, error) {
	root, err := v.load(schemaName)
	if err != nil {
		return nil, err
	}
	issues := make([]Issue, 0)
	validateNode(root, root, value, "$", &issues)
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Keyword < issues[j].Keyword
		}
		return issues[i].Path < issues[j].Path
	})
	return issues, nil
}

func (v *Validator) load(schemaName string) (map[string]any, error) {
	name := path.Base(schemaName)
	v.mu.RLock()
	cached := v.schemas[name]
	v.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	data, err := engineassets.FS.ReadFile("schemas/" + name)
	if err != nil {
		return nil, fmt.Errorf("read embedded schema %q: %w", name, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("parse embedded schema %q: %w", name, err)
	}
	v.mu.Lock()
	v.schemas[name] = decoded
	v.mu.Unlock()
	return decoded, nil
}

func validateNode(root, current map[string]any, value any, instancePath string, issues *[]Issue) {
	if ref, ok := current["$ref"].(string); ok {
		resolved, err := resolveRef(root, ref)
		if err != nil {
			add(issues, instancePath, "$ref", err.Error())
			return
		}
		validateNode(root, resolved, value, instancePath, issues)
		return
	}
	if list, ok := schemaList(current["allOf"]); ok {
		for _, branch := range list {
			validateNode(root, branch, value, instancePath, issues)
		}
	}
	if list, ok := schemaList(current["anyOf"]); ok {
		matches := 0
		for _, branch := range list {
			if matchesSchema(root, branch, value, instancePath) {
				matches++
			}
		}
		if matches == 0 {
			add(issues, instancePath, "anyOf", "value does not match any allowed schema")
		}
	}
	if list, ok := schemaList(current["oneOf"]); ok {
		matches := 0
		for _, branch := range list {
			if matchesSchema(root, branch, value, instancePath) {
				matches++
			}
		}
		if matches != 1 {
			add(issues, instancePath, "oneOf", fmt.Sprintf("value must match exactly one schema; matched %d", matches))
		}
	}
	if forbidden, ok := current["not"].(map[string]any); ok && matchesSchema(root, forbidden, value, instancePath) {
		add(issues, instancePath, "not", "value matches a forbidden schema")
	}
	if condition, ok := current["if"].(map[string]any); ok {
		if matchesSchema(root, condition, value, instancePath) {
			if thenSchema, ok := current["then"].(map[string]any); ok {
				validateNode(root, thenSchema, value, instancePath, issues)
			}
		} else if elseSchema, ok := current["else"].(map[string]any); ok {
			validateNode(root, elseSchema, value, instancePath, issues)
		}
	}
	if spec, exists := current["type"]; exists && !matchesType(spec, value) {
		add(issues, instancePath, "type", fmt.Sprintf("expected %s, got %s", compact(spec), jsonType(value)))
		return
	}
	if expected, exists := current["const"]; exists && !equalJSON(expected, value) {
		add(issues, instancePath, "const", "must equal "+compact(expected))
	}
	if enum, ok := current["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if equalJSON(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			add(issues, instancePath, "enum", "must be one of "+compact(enum))
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		validateObject(root, current, typed, instancePath, issues)
	case []any:
		validateArray(root, current, typed, instancePath, issues)
	case string:
		validateString(current, typed, instancePath, issues)
	default:
		if number, ok := asNumber(value); ok {
			validateNumber(current, number, instancePath, issues)
		}
	}
}

func validateObject(root, current map[string]any, value map[string]any, instancePath string, issues *[]Issue) {
	if min, ok := asInt(current["minProperties"]); ok && len(value) < min {
		add(issues, instancePath, "minProperties", fmt.Sprintf("must contain at least %d properties", min))
	}
	if max, ok := asInt(current["maxProperties"]); ok && len(value) > max {
		add(issues, instancePath, "maxProperties", fmt.Sprintf("must contain at most %d properties", max))
	}
	if required, ok := current["required"].([]any); ok {
		for _, raw := range required {
			key, ok := raw.(string)
			if !ok {
				continue
			}
			if _, exists := value[key]; !exists {
				add(issues, join(instancePath, key), "required", "required property is missing")
			}
		}
	}
	if propertyNames, ok := current["propertyNames"].(map[string]any); ok {
		for key := range value {
			validateNode(root, propertyNames, key, join(instancePath, key), issues)
		}
	}
	properties, _ := current["properties"].(map[string]any)
	patternProperties, _ := current["patternProperties"].(map[string]any)
	for key, child := range value {
		matched := false
		if rawSchema, exists := properties[key]; exists {
			if childSchema, ok := rawSchema.(map[string]any); ok {
				validateNode(root, childSchema, child, join(instancePath, key), issues)
			}
			matched = true
		}
		for pattern, rawSchema := range patternProperties {
			re, err := regexp.Compile(pattern)
			if err != nil {
				add(issues, instancePath, "patternProperties", fmt.Sprintf("invalid schema pattern %q", pattern))
				continue
			}
			if re.MatchString(key) {
				if childSchema, ok := rawSchema.(map[string]any); ok {
					validateNode(root, childSchema, child, join(instancePath, key), issues)
				}
				matched = true
			}
		}
		if matched {
			continue
		}
		switch additional := current["additionalProperties"].(type) {
		case bool:
			if !additional {
				add(issues, join(instancePath, key), "additionalProperties", "property is not allowed")
			}
		case map[string]any:
			validateNode(root, additional, child, join(instancePath, key), issues)
		}
	}
}

func validateArray(root, current map[string]any, value []any, instancePath string, issues *[]Issue) {
	if min, ok := asInt(current["minItems"]); ok && len(value) < min {
		add(issues, instancePath, "minItems", fmt.Sprintf("must contain at least %d items", min))
	}
	if max, ok := asInt(current["maxItems"]); ok && len(value) > max {
		add(issues, instancePath, "maxItems", fmt.Sprintf("must contain at most %d items", max))
	}
	if unique, ok := current["uniqueItems"].(bool); ok && unique {
		seen := make(map[string]int)
		for idx, item := range value {
			key := compact(item)
			if first, exists := seen[key]; exists {
				add(issues, fmt.Sprintf("%s[%d]", instancePath, idx), "uniqueItems", fmt.Sprintf("duplicates item at index %d", first))
			} else {
				seen[key] = idx
			}
		}
	}
	if itemSchema, ok := current["items"].(map[string]any); ok {
		for idx, item := range value {
			validateNode(root, itemSchema, item, fmt.Sprintf("%s[%d]", instancePath, idx), issues)
		}
	}
}

func validateString(current map[string]any, value, instancePath string, issues *[]Issue) {
	length := utf8.RuneCountInString(value)
	if min, ok := asInt(current["minLength"]); ok && length < min {
		add(issues, instancePath, "minLength", fmt.Sprintf("must contain at least %d characters", min))
	}
	if max, ok := asInt(current["maxLength"]); ok && length > max {
		add(issues, instancePath, "maxLength", fmt.Sprintf("must contain at most %d characters", max))
	}
	if pattern, ok := current["pattern"].(string); ok {
		re, err := regexp.Compile(pattern)
		if err != nil {
			add(issues, instancePath, "pattern", fmt.Sprintf("schema contains invalid pattern %q", pattern))
		} else if !re.MatchString(value) {
			add(issues, instancePath, "pattern", fmt.Sprintf("must match %q", pattern))
		}
	}
	if format, ok := current["format"].(string); ok && !matchesFormat(format, value) {
		add(issues, instancePath, "format", fmt.Sprintf("must be a valid %s", format))
	}
}

func validateNumber(current map[string]any, value float64, instancePath string, issues *[]Issue) {
	if minimum, ok := asNumber(current["minimum"]); ok && value < minimum {
		add(issues, instancePath, "minimum", fmt.Sprintf("must be >= %v", minimum))
	}
	if maximum, ok := asNumber(current["maximum"]); ok && value > maximum {
		add(issues, instancePath, "maximum", fmt.Sprintf("must be <= %v", maximum))
	}
	if minimum, ok := asNumber(current["exclusiveMinimum"]); ok && value <= minimum {
		add(issues, instancePath, "exclusiveMinimum", fmt.Sprintf("must be > %v", minimum))
	}
	if maximum, ok := asNumber(current["exclusiveMaximum"]); ok && value >= maximum {
		add(issues, instancePath, "exclusiveMaximum", fmt.Sprintf("must be < %v", maximum))
	}
}

func resolveRef(root map[string]any, ref string) (map[string]any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("only local JSON Pointer references are supported, got %q", ref)
	}
	var current any = root
	for _, rawPart := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("reference %q traverses a non-object", ref)
		}
		current, ok = object[part]
		if !ok {
			return nil, fmt.Errorf("reference %q does not exist", ref)
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reference %q does not point to a schema object", ref)
	}
	return resolved, nil
}

func matchesSchema(root, schema map[string]any, value any, instancePath string) bool {
	issues := make([]Issue, 0)
	validateNode(root, schema, value, instancePath, &issues)
	return len(issues) == 0
}

func schemaList(value any) ([]map[string]any, bool) {
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		schema, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		result = append(result, schema)
	}
	return result, true
}

func matchesType(spec, value any) bool {
	switch typed := spec.(type) {
	case string:
		return matchesSingleType(typed, value)
	case []any:
		for _, item := range typed {
			if name, ok := item.(string); ok && matchesSingleType(name, value) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func matchesSingleType(name string, value any) bool {
	switch name {
	case "null":
		return value == nil
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
	case "number":
		_, ok := asNumber(value)
		return ok
	case "integer":
		return isInteger(value)
	default:
		return false
	}
}

func jsonType(value any) string {
	if value == nil {
		return "null"
	}
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	default:
		if isInteger(value) {
			return "integer"
		}
		if _, ok := asNumber(value); ok {
			return "number"
		}
		return fmt.Sprintf("%T", value)
	}
}

func asInt(value any) (int, bool) {
	number, ok := asNumber(value)
	if !ok || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

func asNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

func isInteger(value any) bool {
	switch typed := value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return typed == float32(int64(typed))
	case float64:
		return typed == float64(int64(typed))
	case json.Number:
		_, err := typed.Int64()
		return err == nil
	default:
		return false
	}
}

func equalJSON(left, right any) bool {
	if leftNumber, ok := asNumber(left); ok {
		rightNumber, rightOK := asNumber(right)
		return rightOK && leftNumber == rightNumber
	}
	return reflect.DeepEqual(left, right)
}

func matchesFormat(format, value string) bool {
	switch format {
	case "email":
		address, err := mail.ParseAddress(value)
		return err == nil && address.Address == value && !strings.ContainsAny(value, "<> ")
	case "hostname":
		return validHostname(value)
	case "ipv4":
		ip := net.ParseIP(value)
		return ip != nil && ip.To4() != nil
	case "ipv6":
		ip := net.ParseIP(value)
		return ip != nil && ip.To4() == nil
	case "uri", "uri-reference":
		parsed, err := url.Parse(value)
		return err == nil && (format == "uri-reference" || parsed.IsAbs())
	case "date-time":
		_, err := time.Parse(time.RFC3339, value)
		return err == nil
	default:
		return true
	}
}

func validHostname(value string) bool {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if value == "" || len(value) > 253 || strings.Contains(value, "_") {
		return false
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}
	labelPattern := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	for _, label := range labels {
		if !labelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func join(parent, key string) string {
	if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`).MatchString(key) {
		return parent + "." + key
	}
	return parent + "[" + strconv.Quote(key) + "]"
}

func add(issues *[]Issue, path, keyword, message string) {
	*issues = append(*issues, Issue{Path: path, Keyword: keyword, Message: message})
}

func compact(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}
