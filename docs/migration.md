# Manual migration from mkserver scripts

Automatic migration is intentionally out of scope. Cut over manually after the
corresponding milestone is implemented and tested in a separate environment.

1. Inventory current domains, ports, volumes, users, databases, and mail policy.
2. Translate non-secret configuration into `deployment.yaml` and tenant files.
3. Move generated passwords and API credentials into SOPS/age or SSM references.
4. Provision parallel infrastructure and join hosts to the Headscale mesh.
5. Restore databases and file data into new named volumes or bind mounts.
6. Validate application health over the mesh before changing public DNS.
7. Lower TTLs, cut DNS over, monitor, then retire legacy scripts and hosts.

The legacy WireGuard NAT/forwarding recipe is retained conceptually as the
optional `advertise_exitnode` mesh-node feature; WireGuard server installation
scripts are not ported directly.
