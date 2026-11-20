# Blueprints

A **blueprint** is a repeatable path for putting a tenant-owned application on
the mksrv fleet: who does what, in which order, how each step is verified, and
which traps to avoid. It is distilled from real deployments, not designed in the
abstract.

> **Status: draft.** Built from two cases, HG-LIMS (done) and SciNugget (in
> progress). Every claim is marked **Verified** (seen working, or read in code)
> or **To verify** (a plan or an assumption). Treat the second kind as a
> to-do, not as fact.

| Document | What it is |
|---|---|
| [Tenant app on the fleet](tenant-app-on-fleet.md) | The generic blueprint: actors, service catalog, steps 0–9, deploying **without a staging environment**, migrations, rollback, known traps |
| [The call to action](agent-call-to-action.md) | What to hand a tenant's developer or agent before anything is deployed: context, steps, rights and obligations, and the request/answer protocol |
| [Case: HG-LIMS](cases/hg-lims.md) | The first deployment, as a retrospective: what we did, what broke, what we changed in mksrv |
| [Case: SciNugget](cases/scinugget.md) | The second one, in progress: a Next.js app with a queue worker, mapped step by step, with the gaps found before deploying |

## How it relates to the rest of the docs

Blueprints cut across the four Diátaxis kinds on purpose: they tell a story
(explanation), list steps (how-to) and cite facts (reference). They link to the
[ADRs](../explanation/decisions/) for the *why* and to the
[how-to guides](../how-to/) and [reference](../reference/) for the exact fields
and commands, and avoid repeating them.

## How a case improves the blueprint

1. A case starts from the generic blueprint and fills it in for one app.
2. Every surprise becomes an entry under the case's **Gaps** (with a status).
3. A gap that bites twice is promoted to the blueprint's **Known traps**; one
   that needs code becomes a mksrv change with its own ADR.
4. Short, reusable facts ("decision, trap, verification command") will move to
   a `cards/` directory when there are enough of them to be worth indexing.
