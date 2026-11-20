# Blueprint: the call to action for a tenant's agent

What to hand to the developer, or the coding agent, of a tenant-owned app before
anything is deployed: the context the fleet offers, the steps, and the rights and
obligations on both sides, in one place they can act from. Status: **draft**
(see the [README](README.md) for the markers). First instance:
[SciNugget](cases/scinugget.md).

## 1. When to send one

When a tenant-owned app is about to be put on the fleet and its developers work
apart from the operator. It replaces a long conversation with a document they can
re-read, and it keeps both sides to the same facts.

## 2. What a call to action contains

| Section | Purpose |
|---|---|
| **Context** | What the fleet is, what it offers, and what is already decided for this app |
| **Read first** | The exact documents, with paths, in reading order. State what is authoritative |
| **Steps** | The [deployment steps](tenant-app-on-fleet.md#3-the-steps), marked with what is done, what is next, and who acts |
| **Rights** | What the tenant can expect from the operator |
| **Obligations** | What the tenant owes: secrets, privilege, migrations, availability |
| **Limits** | What neither side does |
| **Work now** | The tasks the agent can start without waiting for anything |
| **Protocol** | How the two sides talk (below) |
| **Open decisions** | What blocks which step, so the agent does not block on the rest |

## 3. The contract

### Rights of the tenant

1. **The services it declares** (database, object storage, sign-in, secrets, a
   public hostname with TLS) are provisioned by the operator when the tenant's
   YAML is applied, and the credentials arrive scoped and wrapped.
2. **Least privilege, by construction:** read-only roles bound to the node's
   address; no fleet credential, no shell on the fleet.
3. **The operator does not touch the code or the data.** It does not read the
   tenant's secrets.
4. **Notice of platform changes that affect it** (for example a change to how
   Postgres admits tailnet clients), in writing, before they happen.
5. **A written answer** to each written request, and the decision recorded in the
   docs when it is a design decision.

### Obligations of the tenant

1. **The app and its availability are the tenant's.** The fleet gives services,
   not uptime of the application.
2. **Secrets live only in OpenBao.** Never in the repository, an image, a
   package, a log or a conversation; never printed. SecretIDs are wrapped,
   single-use and rotated (90 days by default).
3. **One identity per app.** An AppRole's CIDR binds the node, not the app.
4. **No more privilege than needed.** Do not ask for, or use, the PostgREST
   authenticator; ask for a service role instead.
5. **Migrations are backward compatible,** run after a backup, and the rollback
   has been **performed once** before the first public release.
6. **A health endpoint that reflects real dependencies,** and a known failure
   mode for each dependency.
7. **No development bypass reaches production.**
8. **Requests go in writing,** one per topic, and the tenant does not assume an
   answer. Surprises are reported, so they feed the blueprints.

### Limits (both sides)

- Only the tenant's own resources. No other tenant's, ever.
- No mail DNS records are created or edited without the owner's explicit word.
- Nothing is applied to the fleet without the operator's confirmation.

## 4. The protocol

Two directories of plain files, read by whoever is working at the time:

- `inbox/request-<topic>.md`: the tenant's agent asks the operator for something.
- `inbox/answer-<topic>.md`: the operator's agent answers, with what it did and
  what it did **not** verify.

The names follow the tenant's language (SciNugget uses `peticion-mksrv-<tema>.md`
and `respuesta-mksrv-<tema>.md`, as HG-LIMS did); what matters is one file per
topic and that every answer says what it left unverified.

Each request states: what is needed, why, what it depends on, and what it can do
in the meantime. Each answer states: what changed, how to check it, and what is
still pending on whom.

## 5. Skeleton

```markdown
# Call to action: <app> on the fleet
## Context          (what the fleet offers; what is decided; what is not)
## Read first       (paths, in order; which is authoritative)
## Steps            (table: step, state, who acts, next action)
## Rights and obligations   (the contract above, adapted)
## Work now         (tasks that need nothing from the operator)
## Protocol         (request-/answer- files)
## Open decisions   (each with the step it blocks)
```
