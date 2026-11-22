# Package placeholder

M2 bootstrap, checksum-aware deployment, hooks, and health.

This directory is intentionally non-executable in M0.

## Rewriting files a container bind-mounts

Files are normally written to a temp file and renamed, which is atomic but gives the path a new inode. A container
that bind-mounts a single file (`Volume=<host file>:<container file>`) keeps the old inode, so a changed config is
never seen, and a reload re-reads the stale file and reports success (Prometheus ran with a ten-week-old
`rules.yml` that way). `DeployStack` therefore detects the rendered files that a rendered Quadlet mounts this way
(`bindMountedFiles`) and rewrites them in place (`writeStackFile`), keeping the inode. A container that already
holds an old inode (started before the file was last replaced) is not repaired by this; restart it once.

