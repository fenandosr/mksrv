// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fenandosr/mksrv/internal/model"
	sshx "github.com/fenandosr/mksrv/internal/ssh"
)

// Tenant `storage:` (ADR 0041): the S3 credential mirrored into OpenBao KV and
// the extra, narrow `svc-<id>-<name>` AppRoles that read it.

// storageSecretName is the KV entry (kv/tenants/<id>/storage-s3) the tenant's
// application resolves its S3 credential from.
const storageSecretName = "storage-s3"

// tenantAppRoleNames returns every extra AppRole name (`svc-<id>-<name>`) of
// the tenants that consume openbao, sorted. It feeds the mksrv-operator policy
// (operatorAppRolePolicyHCL), which needs one explicit block per role.
func tenantAppRoleNames(tenants map[string]model.Tenant) []string {
	var names []string
	for _, id := range openbaoTenantIDs(tenants) {
		if st := tenants[id].Storage; st != nil {
			for _, r := range st.AppRoles {
				names = append(names, r.AppRoleName(id))
			}
		}
	}
	sort.Strings(names)
	return names
}

// tenantAppRolePolicyHCL is read-only over the listed secret names under the
// tenant's own kv/tenants/<id>/ subtree. The schema restricts each entry to
// `[a-z0-9-]` plus an optional trailing `*`, so it cannot climb out of the
// subtree; nothing here grants list, Transit, or write.
func tenantAppRolePolicyHCL(id string, r model.TenantAppRole) string {
	var b strings.Builder
	for _, p := range r.KVReadPaths() {
		fmt.Fprintf(&b, "path \"kv/data/tenants/%s/%s\" {\n  capabilities = [\"read\"]\n}\n", id, p)
	}
	return b.String()
}

// tenantAppRoleWriteArgs is the `bao write auth/approle/role/<name>` argv.
// Both the SecretID and the tokens it mints are bound to the declared CIDRs.
func tenantAppRoleWriteArgs(id string, r model.TenantAppRole) []string {
	name := r.AppRoleName(id)
	cidrs := strings.Join(r.CIDRs, ",")
	return []string{
		"write", "auth/approle/role/" + name,
		"token_policies=" + name,
		"token_ttl=1h", "token_max_ttl=4h",
		"token_bound_cidrs=" + cidrs,
		"secret_id_bound_cidrs=" + cidrs,
		"secret_id_ttl=" + strconv.Itoa(r.SecretIDTTL()*24) + "h",
		"secret_id_num_uses=0",
	}
}

// storageSecretJSON is the stdin document for `bao kv put <path> -`. The
// secret goes over stdin, never argv: sshx error text quotes the whole command.
func storageSecretJSON(accessKeyID, secretAccessKey string) []byte {
	b, _ := json.Marshal(map[string]string{
		"access_key_id":     accessKeyID,
		"secret_access_key": secretAccessKey,
	})
	return b
}

// provisionTenantStorage mirrors the tenant's Terraform-created S3 credential
// to SSM and OpenBao KV, and writes its extra AppRoles. Idempotent; the KV
// entry is only rewritten when the stored secret_access_key has drifted (a
// rotation re-creates the access key in Terraform), because KV v2 keeps a
// version per write.
func (f *fleet) provisionTenantStorage(ctx context.Context, client *sshx.Client, rootToken, id string) error {
	st := f.data.Tenants[id].Storage
	if st == nil {
		return nil
	}
	if st.S3 != nil {
		out, ok := f.outputs.TenantStorageS3[id]
		if !ok {
			return fmt.Errorf("openbao %s: no S3 credential in the infra outputs (run mksrv apply --infra-only first)", id)
		}
		if err := f.resolver.Put(ctx, "/mksrv/{env}/storage/tenant_"+id+"_s3_access_key_id", out.AccessKeyID); err != nil {
			return fmt.Errorf("openbao %s: store s3 access key id: %w", id, err)
		}
		if err := f.resolver.Put(ctx, "/mksrv/{env}/storage/tenant_"+id+"_s3_secret_access_key", out.SecretAccessKey); err != nil {
			return fmt.Errorf("openbao %s: store s3 secret access key: %w", id, err)
		}
		path := "kv/tenants/" + id + "/" + storageSecretName
		cur, _ := client.Run(ctx, baoExec(rootToken, "kv", "get", "-format=json", path))
		if baoKVField(cur.Stdout, "secret_access_key") != out.SecretAccessKey ||
			baoKVField(cur.Stdout, "access_key_id") != out.AccessKeyID {
			if _, err := client.RunInput(ctx, baoExec(rootToken, "kv", "put", path, "-"),
				storageSecretJSON(out.AccessKeyID, out.SecretAccessKey)); err != nil {
				return fmt.Errorf("openbao %s: mirror %s secret: %w", id, storageSecretName, err)
			}
		}
	}
	for _, r := range st.AppRoles {
		name := r.AppRoleName(id)
		if _, err := client.RunInput(ctx,
			baoExec(rootToken, "policy", "write", name, "-"), []byte(tenantAppRolePolicyHCL(id, r)),
		); err != nil {
			return fmt.Errorf("openbao %s: write policy %s: %w", id, name, err)
		}
		if _, err := client.Run(ctx, baoExec(rootToken, tenantAppRoleWriteArgs(id, r)...)); err != nil {
			return fmt.Errorf("openbao %s: write approle %s: %w", id, name, err)
		}
		// No bootstrap SecretID: mksrv never needs this credential, only the
		// tenant's service does, via `mksrv tenant secret-id <id> --approle`.
	}
	return nil
}
