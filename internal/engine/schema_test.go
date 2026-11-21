// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"

	engineassets "github.com/fenandosr/mksrv"
	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/schema"
)

func testCatalog(t *testing.T) map[string]model.Stack {
	t.Helper()
	catalog, err := Catalog(schema.New())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	return catalog
}

func compileEffective(t *testing.T, name string, catalog map[string]model.Stack) *jsonschema.Schema {
	t.Helper()
	data, err := EffectiveSchema(name, catalog)
	if err != nil {
		t.Fatalf("EffectiveSchema(%s) error = %v", name, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s: not valid JSON: %v", name, err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err := c.AddResource("mem:///"+name+".json", doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	compiled, err := c.Compile("mem:///" + name + ".json")
	if err != nil {
		t.Fatalf("%s: the effective schema does not compile: %v", name, err)
	}
	return compiled
}

func validateYAMLAgainst(t *testing.T, compiled *jsonschema.Schema, doc string) error {
	t.Helper()
	asJSON, err := yaml.YAMLToJSON([]byte(doc))
	if err != nil {
		t.Fatalf("yaml: %v", err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	return compiled.Validate(value)
}

func TestEffectiveSchemasCompileForEveryName(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t)
	for _, name := range SchemaNames {
		compileEffective(t, name, catalog)
	}
	if _, err := EffectiveSchema("nope", catalog); err == nil || !strings.Contains(err.Error(), "unknown schema") {
		t.Fatalf("an unknown name must say so, got %v", err)
	}
}

// enumAt reads the enum at a JSON-pointer-like path of the generated schema.
func enumAt(t *testing.T, name string, catalog map[string]model.Stack, path string) []string {
	t.Helper()
	data, err := EffectiveSchema(name, catalog)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	n, err := node(doc, path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := n["enum"].([]any)
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i] = v.(string)
	}
	return out
}

func catalogNames(catalog map[string]model.Stack, keep func(model.Stack) bool) []string {
	var names []string
	for n, s := range catalog {
		if keep(s) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func TestStackEnumsEqualTheCatalog(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t)

	host := enumAt(t, "deployment", catalog, "/$defs/host/properties/stacks/items")
	if want := catalogNames(catalog, func(s model.Stack) bool { return !s.LocalHostsOnly }); !slices.Equal(host, want) {
		t.Fatalf("host stacks = %v, want %v (a stack added to the catalog must be suggested)", host, want)
	}
	for _, mustHave := range []string{"openbao", "postgres", "pgproxy", "agent"} {
		if !slices.Contains(host, mustHave) {
			t.Errorf("%s missing from the host stack suggestions", mustHave)
		}
	}

	tenant := enumAt(t, "tenant", catalog, "/properties/stacks/items")
	if want := catalogNames(catalog, func(s model.Stack) bool { return s.PerTenant && !s.LocalHostsOnly }); !slices.Equal(tenant, want) {
		t.Fatalf("tenant stacks = %v, want %v", tenant, want)
	}
	if slices.Contains(tenant, "postgres") || slices.Contains(tenant, "pgproxy") {
		t.Errorf("a fleet-wide stack must not be offered to a tenant: %v", tenant)
	}

	local := enumAt(t, "tenant", catalog, "/properties/local_hosts/additionalProperties/properties/stacks/items")
	if want := catalogNames(catalog, func(s model.Stack) bool { return s.LocalHostsOnly }); !slices.Equal(local, want) || len(local) == 0 {
		t.Fatalf("local_hosts stacks = %v, want %v", local, want)
	}
	for _, s := range local {
		if slices.Contains(host, s) {
			t.Errorf("local-only stack %s must not be offered for a fleet host", s)
		}
	}
}

func TestInstanceTypesAreSuggestedButNotEnforced(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t)
	compiled := compileEffective(t, "deployment", catalog)
	base := `
version: 1
engine: dev
env: prod
timezone: Etc/UTC
mgmt_cidr: auto
aws: {region: us-east-1}
backend: {type: s3, bucket: b, lock_table: l, region: us-east-1}
dns: {provider: manual, root_domain: example.com}
identity: {keycloak_domain: auth.example.com, headscale_domain: vpn.example.com, configd_domain: cfg.example.com}
hosts:
  h1:
    provider: aws
    instance_type: %s
    stacks: [base]
`
	// Validity of the REST of the document is not what is under test: compare the
	// schema errors that mention instance_type.
	mentions := func(typ string) bool {
		err := validateYAMLAgainst(t, compiled, strings.Replace(base, "%s", typ, 1))
		return err != nil && strings.Contains(err.Error(), "instance_type")
	}
	for _, ok := range []string{"t4g.small", "m7g.large", "t2.micro", "x2gd.xlarge", "c7a.4xlarge"} {
		if mentions(ok) {
			t.Errorf("%s must be accepted (suggested or matching the pattern)", ok)
		}
	}
	if !mentions("NOT A TYPE!") {
		t.Error("a value that does not match the pattern must still be rejected")
	}
	if got := enumAt2(t, catalog); !slices.Contains(got, "t4g.small") || !slices.Contains(got, "m7g.large") {
		t.Errorf("t4g.small and m7g.large must be among the suggestions, got %d entries", len(got))
	}
}

// enumAt2 reads the instance-type suggestions (the enum inside the anyOf).
func enumAt2(t *testing.T, catalog map[string]model.Stack) []string {
	t.Helper()
	data, err := EffectiveSchema("deployment", catalog)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(data, &doc)
	n, err := node(doc, "/$defs/host/properties/instance_type")
	if err != nil {
		t.Fatal(err)
	}
	branches, _ := n["anyOf"].([]any)
	if len(branches) != 2 {
		t.Fatalf("instance_type must be anyOf [enum, pattern], got %v", n["anyOf"])
	}
	var out []string
	for _, v := range branches[0].(map[string]any)["enum"].([]any) {
		out = append(out, v.(string))
	}
	return out
}

func TestATypoInAStackNameIsFlagged(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t)
	tenant := compileEffective(t, "tenant", catalog)
	doc := `
version: 1
id: acme
display_name: ACME
base_domain: acme.example.com
stacks: [%s]
`
	if err := validateYAMLAgainst(t, tenant, strings.Replace(doc, "%s", "database, openbao", 1)); err != nil {
		t.Fatalf("real stacks must pass: %v", err)
	}
	err := validateYAMLAgainst(t, tenant, strings.Replace(doc, "%s", "database, postgress", 1))
	if err == nil || !strings.Contains(err.Error(), "must be one of") || !strings.Contains(err.Error(), "/stacks/1") {
		t.Fatalf("a misspelled stack must fail the enum at stacks[1], got %v", err)
	}
	// A fleet-wide stack is not something a tenant consumes.
	if err := validateYAMLAgainst(t, tenant, strings.Replace(doc, "%s", "pgproxy", 1)); err == nil {
		t.Fatal("a tenant must not be offered, nor accept, a fleet-wide stack")
	}
}

func TestTheExampleWorkspaceValidatesAgainstTheEffectiveSchemas(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t)
	for name, file := range map[string]string{
		"deployment": "examples/workspace/deployment.yaml",
		"tenant":     "examples/workspace/tenants/acme.yaml",
		"users":      "examples/workspace/tenants/acme.users.yaml",
	} {
		raw, err := engineassets.FS.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if err := validateYAMLAgainst(t, compileEffective(t, name, catalog), string(raw)); err != nil {
			t.Errorf("%s does not validate against the effective %s schema: %v", file, name, err)
		}
	}
}

func TestDescriptionsAreInjectedAndTheBaseSchemaIsUntouched(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t)
	data, err := EffectiveSchema("tenant", catalog)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(data, &doc)
	n, err := node(doc, "/properties/base_domain")
	if err != nil || n["description"] == "" || n["description"] == nil {
		t.Fatalf("base_domain has no description: %v %v", n, err)
	}
	// The embedded base schema is read-only input: it must carry neither the enum nor the text.
	base, _ := engineassets.FS.ReadFile("schemas/tenant.v1.json")
	if bytes.Contains(base, []byte("enumDescriptions")) || bytes.Contains(base, []byte("markdownDescription")) {
		t.Fatal("the generator must not modify the base schema")
	}
	if !strings.Contains(doc["$comment"].(string), "mksrv schema tenant") {
		t.Fatalf("the generated file must say how to regenerate it, got %v", doc["$comment"])
	}
}
