# cache stack templates

- `mksrv-cache.network.tmpl` — the `mksrv-cache` Podman network.
- `redis.conf.tmpl` — Redis config: AOF, 256 MB `allkeys-lru`, `aclfile`.
- `users.acl.tmpl` — ACL seed (disabled `default`, `mksrv` admin), rendered to
  `acl/users.acl`. `mksrv tenant apply` rewrites this file with one `user <id>`
  line per tenant, confined to the `<id>:*` key/channel namespace, then runs
  `ACL LOAD`. A bare `mksrv deploy` reverts it to the seed until the next
  `tenant apply`. Redis's external `aclfile` parser accepts only `user …` and
  blank lines — **no `#` comments** — so this template must not have any. The
  container bind-mounts the `acl/` **directory**, not the file: the file is
  replaced by atomic rename on every rewrite, and a single-file mount would pin
  the container to the original inode (`ACL LOAD` would keep reading stale data).
- `redis.container.tmpl` — the shared Redis 7 container, published on the private
  VPC IP, the tailnet IP, and loopback (port 6379).
