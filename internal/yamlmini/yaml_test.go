// SPDX-License-Identifier: Apache-2.0

package yamlmini

import (
	"reflect"
	"testing"
)

func TestParseWorkspaceSubset(t *testing.T) {
	t.Parallel()
	value, err := Parse([]byte(`
version: 1
telemetry: { enabled: false }
hosts:
  edge:
    provider: aws
    stacks: [base, identity]
users:
  - email: jane@example.com
    name: "Jane Doe"
    groups:
      - apps
      - cluster
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	root := value.(map[string]any)
	edge := root["hosts"].(map[string]any)["edge"].(map[string]any)
	if !reflect.DeepEqual(edge["stacks"], []any{"base", "identity"}) {
		t.Fatalf("stacks = %#v", edge["stacks"])
	}
	users := root["users"].([]any)
	if users[0].(map[string]any)["email"] != "jane@example.com" {
		t.Fatalf("users = %#v", users)
	}
}

func TestRejectsAdvancedYAML(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]byte("value: &anchor nope\n")); err == nil {
		t.Fatal("Parse() expected error")
	}
}

func TestKeepsHashInQuotedValue(t *testing.T) {
	t.Parallel()
	value, err := Parse([]byte("primary: \"#0C6D77\" # comment\n"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if value.(map[string]any)["primary"] != "#0C6D77" {
		t.Fatalf("value = %#v", value)
	}
}
