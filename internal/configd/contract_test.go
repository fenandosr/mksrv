// SPDX-License-Identifier: Apache-2.0

package configd

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/schema"
	xssh "golang.org/x/crypto/ssh"
)

// The clientconfig contract is shared with the Cloud-IT VPN desktop app, whose
// repo keeps the canonical copy (schemas/clientconfig.v1.json) and requires the
// two to be byte-identical (its scripts/check-schema-sync.sh <path-to-this-file>).
// The recorded hash lets mksrv notice an accidental local edit.
func TestClientConfigSchemaMatchesRecordedHash(t *testing.T) {
	t.Parallel()
	blob, err := os.ReadFile("../../schemas/clientconfig.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile("../../schemas/clientconfig.v1.sha256")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(blob)
	if got, want := hex.EncodeToString(sum[:]), strings.TrimSpace(string(recorded)); got != want {
		t.Fatalf("schemas/clientconfig.v1.json hash %s != recorded %s: re-sync it from the Cloud-IT VPN repo", got, want)
	}
}

// What configd emits must validate against the contract the desktop app enforces
// (additionalProperties: false everywhere), with and without the optional ssh.user.
func TestEmittedClientConfigValidatesAgainstContract(t *testing.T) {
	t.Parallel()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := xssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	hostKey := strings.TrimSpace(string(xssh.MarshalAuthorizedKey(sp)))
	for name, c := range map[string]struct {
		user string
		keys []string
	}{
		"without ssh.user":       {},
		"with ssh.user":          {user: "jdoe"},
		"with hostKeys":          {keys: []string{hostKey}},
		"with user and hostKeys": {user: "jdoe", keys: []string{hostKey}},
	} {
		user := c.user
		cfg := testConfig()
		cfg.Forwards = append(cfg.Forwards, Forward{
			ID: "login", Label: "Login", Type: "ssh",
			Listen: Listen{Host: "127.0.0.1"}, PortStrategy: "auto",
			Target: "login.prod.mksrv:22", OpenAction: OpenAction{Kind: "ssh-terminal"},
			HealthCheck: HealthCheck{Kind: "tcp", IntervalSec: 30}, MaxConns: 16,
			SSH: &SSH{HostKeyAlias: "login.prod.mksrv", WriteConfig: true, ConfigAlias: "login", KnownHostsFile: "login", User: user, HostKeys: c.keys},
		})
		blob, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if has := strings.Contains(string(blob), `"user"`); has != (user != "") {
			t.Fatalf("%s: user emitted=%v, want %v: %s", name, has, user != "", blob)
		}
		if has := strings.Contains(string(blob), `"hostKeys"`); has != (len(c.keys) > 0) {
			t.Fatalf("%s: hostKeys emitted=%v, want %v: %s", name, has, len(c.keys) > 0, blob)
		}
		var value any
		if err := json.Unmarshal(blob, &value); err != nil {
			t.Fatal(err)
		}
		issues, err := schema.New().Validate("clientconfig.v1.json", value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(issues) != 0 {
			t.Fatalf("%s: emitted clientconfig violates the contract: %+v\n%s", name, issues, blob)
		}
	}
}
