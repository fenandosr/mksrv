// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fenandosr/mksrv/internal/model"
	"github.com/fenandosr/mksrv/internal/workspace"
)

func TestAssignStablePorts(t *testing.T) {
	t.Parallel()
	// No lock entry yet: sorted-id order from the base, as before.
	first, err := assignStablePorts([]string{"a", "b", "c"}, 8030, 10, nil, "Nextcloud")
	if err != nil || first["a"] != 8030 || first["b"] != 8031 || first["c"] != 8032 {
		t.Fatalf("seed = %v, %v", first, err)
	}
	// A tenant that sorts first takes the next free port and moves nobody.
	after, err := assignStablePorts([]string{"0", "a", "b", "c"}, 8030, 10, first, "Nextcloud")
	if err != nil || after["a"] != 8030 || after["b"] != 8031 || after["c"] != 8032 || after["0"] != 8033 {
		t.Fatalf("after adding a first tenant = %v, %v", after, err)
	}
	// A removed tenant frees its port for the next newcomer.
	reuse, _ := assignStablePorts([]string{"a", "c", "z"}, 8030, 10, after, "Nextcloud")
	if reuse["a"] != 8030 || reuse["c"] != 8032 || reuse["z"] != 8031 {
		t.Fatalf("reuse = %v", reuse)
	}
	// A pinned port outside the range (hand-edited lock) is reassigned, not trusted.
	out, _ := assignStablePorts([]string{"a"}, 8030, 10, map[string]int{"a": 9999}, "Nextcloud")
	if out["a"] != 8030 {
		t.Fatalf("out-of-range pin kept: %v", out)
	}
	// Running out of range is an error, not a silent collision.
	ids := make([]string, 11)
	for i := range ids {
		ids[i] = string(rune('a' + i))
	}
	if _, err := assignStablePorts(ids, 8030, 10, nil, "Nextcloud"); err == nil || !strings.Contains(err.Error(), "no free Nextcloud port left") {
		t.Fatalf("exhaustion error = %v", err)
	}
}

// files ports are pinned in mksrv.lock: a tenant that sorts first must not move
// the others, and the pin survives a reload from disk.
func TestFilesPortsPinnedInLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mk := func(ids ...string) map[string]model.Tenant {
		m := map[string]model.Tenant{}
		for _, id := range ids {
			m[id] = model.Tenant{ID: id, Stacks: []string{"files"}}
		}
		m["nofiles"] = model.Tenant{ID: "nofiles", Stacks: []string{"database"}}
		return m
	}
	f := &fleet{data: workspace.Data{Root: root, Tenants: mk("mcps", "hg")}}
	nc, smb, err := f.filesPorts()
	if err != nil || nc["hg"] != 8030 || nc["mcps"] != 8031 || smb["hg"] != 4450 || smb["mcps"] != 4451 || len(nc) != 2 {
		t.Fatalf("first run nc=%v smb=%v err=%v", nc, smb, err)
	}
	blob, err := os.ReadFile(filepath.Join(root, "mksrv.lock"))
	if err != nil || !strings.Contains(string(blob), "files_nextcloud_ports") || !strings.Contains(string(blob), "files_samba_ports") {
		t.Fatalf("lock not written: %s %v", blob, err)
	}
	// A tenant that sorts before both arrives later: existing ports stay put.
	f.data.Tenants = mk("mcps", "hg", "acme")
	nc, smb, err = f.filesPorts()
	if err != nil || nc["hg"] != 8030 || nc["mcps"] != 8031 || nc["acme"] != 8032 || smb["acme"] != 4452 {
		t.Fatalf("after adding acme nc=%v smb=%v err=%v", nc, smb, err)
	}
}
