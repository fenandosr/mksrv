// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestBaoInitParse(t *testing.T) {
	t.Parallel()
	const out = `{
	  "unseal_keys_b64": [],
	  "unseal_shares": 1,
	  "recovery_keys_b64": ["a2V5MQ==", "a2V5Mg==", "a2V5Mw=="],
	  "recovery_keys_shares": 5,
	  "recovery_keys_threshold": 3,
	  "root_token": "s.abc123"
	}`
	var init baoInit
	if err := json.Unmarshal([]byte(out), &init); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if init.RootToken != "s.abc123" {
		t.Fatalf("root token = %q", init.RootToken)
	}
	if len(init.RecoveryKeysB64) != 3 || init.RecoveryKeysB64[0] != "a2V5MQ==" {
		t.Fatalf("recovery keys = %#v", init.RecoveryKeysB64)
	}
}

func TestBaoExec(t *testing.T) {
	t.Parallel()
	plain := baoExec("", "status", "-format=json")
	if !strings.Contains(plain, "podman exec -e BAO_ADDR=http://127.0.0.1:8200 mksrv-openbao bao") ||
		!strings.Contains(plain, "'status' '-format=json'") {
		t.Fatalf("plain: %s", plain)
	}
	withTok := baoExec("s.tok", "secrets", "list")
	if !strings.Contains(withTok, "-e BAO_TOKEN='s.tok'") {
		t.Fatalf("token not injected: %s", withTok)
	}
}

func TestNewOpenBaoCommand(t *testing.T) {
	t.Parallel()
	cmd := (&App{}).newOpenBaoCommand(&globalOptions{})
	if cmd.Use != "openbao" {
		t.Fatalf("use = %q", cmd.Use)
	}
	var subs []string
	for _, c := range cmd.Commands() {
		subs = append(subs, c.Name())
	}
	for _, want := range []string{"bootstrap", "status"} {
		if !slices.Contains(subs, want) {
			t.Fatalf("subcommand %q missing from %v", want, subs)
		}
	}
}
