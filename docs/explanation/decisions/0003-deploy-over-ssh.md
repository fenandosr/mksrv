# ADR 0003: Deploy over SSH instead of introducing configuration management

- Status: Accepted
- Date: 2026-08-27

## Context

Operators need one distributable CLI, idempotent host bootstrap, checksum-aware
file transfer, systemd/Quadlet activation, logs, and health checks. Adding Ansible
or another agent would create a second runtime and inventory model.

## Decision

Use Go SSH and SFTP clients from the CLI. Bootstrap scripts are embedded,
versioned, idempotent, and marker-guarded. Stack files are rendered locally,
compared with a remote manifest, and only changed units are restarted.

## Consequences

The operator needs reachable SSH management addresses and credentials, but fleet
hosts require no mksrv agent. The deploy layer must implement careful retries,
redaction, cancellation, host-key policy, and bounded connection pooling.
