# Shell completion

`mksrv completion <shell>` prints a completion script for bash, zsh, fish or powershell. The script is generated
by the installed binary (nothing is committed), so it always matches that version's commands and flags.

```sh
# bash (once per session; put it in ~/.bashrc to keep it)
source <(mksrv completion bash)

# zsh
mksrv completion zsh > "${fpath[1]}/_mksrv"     # then start a new shell

# fish
mksrv completion fish > ~/.config/fish/completions/mksrv.fish
```

Run `mksrv completion <shell> --help` for the details of your shell.

## What completes from your workspace

Beyond command and flag names, TAB offers values read from the workspace (found the same way as for any
command, from the current directory or `--workspace`):

| Where | Offers |
|---|---|
| `mksrv tenant apply`, `users apply`, `tenant secret-id`, `tenant mesh` | tenant ids (`secret-id` and `mesh` take one; `apply` takes several and does not repeat one) |
| `mksrv deploy` | host names |
| `--stack` (comma lists work: `--stack base,ba<TAB>`) | stack names from the catalog |
| `mksrv tenant secret-id <id> --approle` | the `storage.approles` names of that tenant |

Completion reads only local files: it never calls AWS, a host or the network, and with no workspace (or one
it cannot read) it offers nothing and prints nothing.

Mutually exclusive flags (`--service` with `--approle`, `--list` with `--revoke`) are rejected as a usage error
before anything runs.
