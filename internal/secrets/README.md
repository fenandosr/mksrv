# secrets

AWS SSM Parameter Store (`SecureString`) resolution. `Resolver.Get` reads an
existing value; `Resolver.EnsureRandom` generates and stores one only if the
parameter doesn't already exist (`Overwrite: false`), so it never clobbers a
value a human or a migration already seeded. `{env}` in a ref is expanded from
the workspace's own environment name.

The original design (ADR 0010) planned a workspace-local `secrets.sops.yaml`
(SOPS + age) alongside SSM. That half was never built — SSM is the only
resolution path.
