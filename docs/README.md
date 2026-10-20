# mksrv documentation

Organized along [Diátaxis](https://diataxis.fr/), which separates
documentation into four kinds by what the reader needs at that moment: doing
vs. studying, and practical skill vs. theoretical knowledge.

| | Practical skill | Theoretical knowledge |
|---|---|---|
| **Studying** | [Tutorials](tutorials/) — learn by doing, start to finish | [Explanation](explanation/) — understand the design and its history |
| **Doing** | [How-to guides](how-to/) — accomplish a specific task | [Reference](reference/) — look up a fact about the machinery |

- **New to mksrv?** Start with [tutorials/quickstart.md](tutorials/quickstart.md).
- **Have a working workspace and a specific job to do** (enable tenant mail,
  rotate a secret, restore a backup)? Check [how-to/](how-to/).
- **Need the exact shape of something** (a stack's fields, what's scraped,
  an instance's RAM)? Check [reference/](reference/).
- **Want to understand why mksrv is built this way**, or what's implemented
  so far? Check [explanation/](explanation/), including the
  [decision records](explanation/decisions/).

See also the top-level [README](../README.md) for the engine/workspace
boundary and build instructions, and [CONTRIBUTING.md](../CONTRIBUTING.md) for
the development workflow — including where a new ADR belongs.
