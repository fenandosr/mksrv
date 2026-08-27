// SPDX-License-Identifier: Apache-2.0

// Package yamlmini implements the intentionally small YAML subset used by the
// M0 workspace contract. It supports indentation-based maps and sequences,
// quoted/plain scalars, and flow lists/maps. Anchors, tags, merge keys and block
// scalars are rejected. ADR 0006 records the planned upstream-library migration.
package yamlmini

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type sourceLine struct {
	number int
	indent int
	text   string
}

// Unmarshal parses YAML/JSON and decodes it into out through JSON equivalence.
func Unmarshal(data []byte, out any) error {
	value, err := Parse(data)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode parsed YAML: %w", err)
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return fmt.Errorf("decode parsed YAML: %w", err)
	}
	return nil
}

// Parse returns JSON-compatible Go values.
func Parse(data []byte) (any, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("document is empty")
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("parse JSON-compatible YAML: %w", err)
		}
		return normalizeNumbers(value), nil
	}
	lines, err := tokenize(string(data))
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("document has no values")
	}
	if lines[0].indent != 0 {
		return nil, fmt.Errorf("line %d: root value must start at indentation 0", lines[0].number)
	}
	value, next, err := parseBlock(lines, 0, 0)
	if err != nil {
		return nil, err
	}
	if next != len(lines) {
		return nil, fmt.Errorf("line %d: unexpected trailing content", lines[next].number)
	}
	return value, nil
}

func tokenize(input string) ([]sourceLine, error) {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	rawLines := strings.Split(input, "\n")
	result := make([]sourceLine, 0, len(rawLines))
	for idx, raw := range rawLines {
		leading := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if strings.Contains(raw[:leading], "\t") {
			return nil, fmt.Errorf("line %d: tabs are not allowed for indentation", idx+1)
		}
		clean, err := stripComment(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", idx+1, err)
		}
		clean = strings.TrimRightFunc(clean, unicode.IsSpace)
		if strings.TrimSpace(clean) == "" {
			continue
		}
		indent := len(clean) - len(strings.TrimLeft(clean, " "))
		result = append(result, sourceLine{number: idx + 1, indent: indent, text: clean[indent:]})
	}
	return result, nil
}

func stripComment(raw string) (string, error) {
	var quote byte
	escaped := false
	for idx := 0; idx < len(raw); idx++ {
		ch := raw[idx]
		if quote == '"' {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if quote == '\'' {
			if ch == quote {
				if idx+1 < len(raw) && raw[idx+1] == '\'' {
					idx++
					continue
				}
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '#':
			if idx == 0 || unicode.IsSpace(rune(raw[idx-1])) {
				return raw[:idx], nil
			}
		}
	}
	if quote != 0 {
		return "", fmt.Errorf("unterminated quoted scalar")
	}
	return raw, nil
}

func parseBlock(lines []sourceLine, index, indent int) (any, int, error) {
	if index >= len(lines) {
		return nil, index, fmt.Errorf("unexpected end of document")
	}
	if lines[index].indent != indent {
		return nil, index, fmt.Errorf("line %d: expected indentation %d, got %d", lines[index].number, indent, lines[index].indent)
	}
	if isSequenceLine(lines[index].text) {
		return parseSequence(lines, index, indent)
	}
	return parseMapping(lines, index, indent)
}

func parseMapping(lines []sourceLine, index, indent int) (any, int, error) {
	result := make(map[string]any)
	for index < len(lines) {
		line := lines[index]
		if line.indent < indent || isSequenceLine(line.text) {
			break
		}
		if line.indent > indent {
			return nil, index, fmt.Errorf("line %d: unexpected indentation %d", line.number, line.indent)
		}
		keyText, valueText, ok := splitKeyValue(line.text)
		if !ok {
			return nil, index, fmt.Errorf("line %d: expected 'key: value'", line.number)
		}
		key, err := parseKey(keyText)
		if err != nil {
			return nil, index, fmt.Errorf("line %d: %w", line.number, err)
		}
		if _, exists := result[key]; exists {
			return nil, index, fmt.Errorf("line %d: duplicate key %q", line.number, key)
		}
		index++
		if strings.TrimSpace(valueText) == "" {
			if index < len(lines) && lines[index].indent > indent {
				child, next, err := parseBlock(lines, index, lines[index].indent)
				if err != nil {
					return nil, index, err
				}
				result[key] = child
				index = next
			} else {
				result[key] = nil
			}
			continue
		}
		value, err := parseScalar(valueText)
		if err != nil {
			return nil, index, fmt.Errorf("line %d: key %q: %w", line.number, key, err)
		}
		result[key] = value
	}
	return result, index, nil
}

func parseSequence(lines []sourceLine, index, indent int) (any, int, error) {
	result := make([]any, 0)
	for index < len(lines) {
		line := lines[index]
		if line.indent < indent || !isSequenceLine(line.text) {
			break
		}
		if line.indent > indent {
			return nil, index, fmt.Errorf("line %d: unexpected indentation %d", line.number, line.indent)
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line.text, "-"))
		index++
		if rest == "" {
			if index < len(lines) && lines[index].indent > indent {
				child, next, err := parseBlock(lines, index, lines[index].indent)
				if err != nil {
					return nil, index, err
				}
				result = append(result, child)
				index = next
			} else {
				result = append(result, nil)
			}
			continue
		}
		if keyText, valueText, ok := splitKeyValue(rest); ok {
			item := make(map[string]any)
			key, err := parseKey(keyText)
			if err != nil {
				return nil, index, fmt.Errorf("line %d: %w", line.number, err)
			}
			if strings.TrimSpace(valueText) == "" {
				if index < len(lines) && lines[index].indent > indent {
					child, next, err := parseBlock(lines, index, lines[index].indent)
					if err != nil {
						return nil, index, err
					}
					item[key] = child
					index = next
				} else {
					item[key] = nil
				}
			} else {
				value, err := parseScalar(valueText)
				if err != nil {
					return nil, index, fmt.Errorf("line %d: key %q: %w", line.number, key, err)
				}
				item[key] = value
			}
			for index < len(lines) && lines[index].indent > indent {
				extra, next, err := parseBlock(lines, index, lines[index].indent)
				if err != nil {
					return nil, index, err
				}
				extraMap, ok := extra.(map[string]any)
				if !ok {
					return nil, index, fmt.Errorf("line %d: sequence mapping cannot contain a bare sequence", lines[index].number)
				}
				for extraKey, extraValue := range extraMap {
					if _, exists := item[extraKey]; exists {
						return nil, index, fmt.Errorf("line %d: duplicate key %q", lines[index].number, extraKey)
					}
					item[extraKey] = extraValue
				}
				index = next
			}
			result = append(result, item)
			continue
		}
		value, err := parseScalar(rest)
		if err != nil {
			return nil, index, fmt.Errorf("line %d: %w", line.number, err)
		}
		result = append(result, value)
	}
	return result, index, nil
}

func isSequenceLine(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ")
}

func splitKeyValue(input string) (string, string, bool) {
	quote := byte(0)
	escaped := false
	depth := 0
	for idx := 0; idx < len(input); idx++ {
		ch := input[idx]
		if quote == '"' {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if quote == '\'' {
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		case ':':
			if depth == 0 {
				return strings.TrimSpace(input[:idx]), strings.TrimSpace(input[idx+1:]), true
			}
		}
	}
	return "", "", false
}

func parseKey(input string) (string, error) {
	value, err := parseScalar(strings.TrimSpace(input))
	if err != nil {
		return "", err
	}
	key, ok := value.(string)
	if !ok || strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("mapping key must be a non-empty string")
	}
	return key, nil
}

func parseScalar(input string) (any, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", nil
	}
	if strings.HasPrefix(input, "|") || strings.HasPrefix(input, ">") {
		return nil, fmt.Errorf("block scalars are not supported in M0")
	}
	if strings.HasPrefix(input, "&") || strings.HasPrefix(input, "*") || strings.HasPrefix(input, "!") {
		return nil, fmt.Errorf("anchors, aliases and tags are not supported in M0")
	}
	if input[0] == '"' {
		value, err := strconv.Unquote(input)
		if err != nil {
			return nil, fmt.Errorf("invalid double-quoted string: %w", err)
		}
		return value, nil
	}
	if input[0] == '\'' {
		if len(input) < 2 || input[len(input)-1] != '\'' {
			return nil, fmt.Errorf("unterminated single-quoted string")
		}
		return strings.ReplaceAll(input[1:len(input)-1], "''", "'"), nil
	}
	if input[0] == '[' {
		if input[len(input)-1] != ']' {
			return nil, fmt.Errorf("unterminated flow sequence")
		}
		body := strings.TrimSpace(input[1 : len(input)-1])
		if body == "" {
			return []any{}, nil
		}
		parts, err := splitFlow(body, ',')
		if err != nil {
			return nil, err
		}
		values := make([]any, 0, len(parts))
		for _, part := range parts {
			value, err := parseScalar(part)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	}
	if input[0] == '{' {
		if input[len(input)-1] != '}' {
			return nil, fmt.Errorf("unterminated flow mapping")
		}
		body := strings.TrimSpace(input[1 : len(input)-1])
		result := make(map[string]any)
		if body == "" {
			return result, nil
		}
		parts, err := splitFlow(body, ',')
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			keyText, valueText, ok := splitKeyValue(part)
			if !ok {
				return nil, fmt.Errorf("invalid flow mapping entry %q", part)
			}
			key, err := parseKey(keyText)
			if err != nil {
				return nil, err
			}
			if _, exists := result[key]; exists {
				return nil, fmt.Errorf("duplicate flow mapping key %q", key)
			}
			value, err := parseScalar(valueText)
			if err != nil {
				return nil, err
			}
			result[key] = value
		}
		return result, nil
	}
	switch strings.ToLower(input) {
	case "null", "~":
		return nil, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	if value, err := strconv.ParseInt(input, 10, 64); err == nil {
		return value, nil
	}
	if strings.ContainsAny(input, ".eE") {
		if value, err := strconv.ParseFloat(input, 64); err == nil {
			return value, nil
		}
	}
	return input, nil
}

func splitFlow(input string, separator byte) ([]string, error) {
	parts := make([]string, 0)
	start := 0
	quote := byte(0)
	escaped := false
	depth := 0
	for idx := 0; idx < len(input); idx++ {
		ch := input[idx]
		if quote == '"' {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if quote == '\'' {
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced flow collection")
			}
		default:
			if ch == separator && depth == 0 {
				part := strings.TrimSpace(input[start:idx])
				if part == "" {
					return nil, fmt.Errorf("empty flow collection item")
				}
				parts = append(parts, part)
				start = idx + 1
			}
		}
	}
	if quote != 0 || depth != 0 {
		return nil, fmt.Errorf("unterminated flow collection")
	}
	last := strings.TrimSpace(input[start:])
	if last == "" {
		return nil, fmt.Errorf("empty flow collection item")
	}
	return append(parts, last), nil
}

func normalizeNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case []any:
		for idx := range typed {
			typed[idx] = normalizeNumbers(typed[idx])
		}
	case map[string]any:
		for key := range typed {
			typed[key] = normalizeNumbers(typed[key])
		}
	}
	return value
}
