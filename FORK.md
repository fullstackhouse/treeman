# FSH fork of treeman

Upstream: **[stubbedev/treeman](https://github.com/stubbedev/treeman)** (Apache-2.0 / MIT).
This fork exists so Full Stack House can run treeman with binaries **we build from source we
read**, and carry fixes we need before upstream merges them. Nothing here is a criticism of
upstream; see [Why a fork](#why-a-fork).

## The rule that keeps this cheap: additive only

Our diff against upstream is **new files**, plus the smallest possible edits to existing ones.
We do **not** delete the parts we don't use (the MCP server, the TUI, the MySQL/Mongo drivers,
`treeman git`). Dead code costs disk; a 20k-line deletion costs us every future merge.

- `master` — untouched mirror of upstream. Never commit here; it only fast-forwards.
- `fsh` — our branch, and the one we release from. Upstream arrives by **pull request**
  (`fsh-sync.yml`), never by an unattended rebase, so somebody reads the delta before we ship it.

## Why a fork

1. **We don't want to consume upstream's release artifacts.** Upstream's release workflow runs a
   third-party action on a force-moved tag (`stubbedev/xilo@v1`) which `curl`s a `latest` binary
   into `tar` with no checksum, in a job holding `contents: write`. Nothing suggests it has ever
   been abused — it is the same mutable-tag pattern most repos have, ours included — but it means
   a published binary is not cryptographically tied to the source. Building it ourselves makes
   that question moot.
2. **We need a Meilisearch driver**, which upstream has no support for and 7 of our 8 local
   stacks require.
3. **Two safety fixes we would rather not wait for** (see the patch index).

Everything in (1) is fixable upstream with ~5 lines (`actions/attest-build-provenance`, pinning
`xilo@v1`, a `dist/` gitignore entry). We intend to offer those. **If upstream takes them, this
fork's reason to exist shrinks to the Meilisearch driver, and we should say so here and shrink
it.** A fork that outlives its justification is just technical debt with a remote.

## What we actually run

`treeman prepare --no-daemon` from a Conductor `setup` hook, and teardown on `archive`. We do
**not** install the daemon, the launchd/systemd unit, or the MCP server. The daemon *code* still
runs in-process (`--no-daemon` calls `daemon.RunPlanInProcess`), which is fine; what we avoid is
a long-lived background process and a socket.

## Patch index

Every patch is one commit, and one row here. A row without a **Drop when** condition is a bug.

| # | Patch | Why | Upstream | Drop when |
|---|---|---|---|---|
| — | _none yet_ | the branch currently carries only the additive files below | — | — |

Planned, not yet written:

| # | Patch | Why | Upstream | Drop when |
|---|---|---|---|---|
| P1 | Don't cache the full shell env, and tighten the state file mode | `CaptureInheritedEnv` snapshots unfiltered `os.Environ()` and persists it to `~/.local/share/treeman/treeman.db`, created `0644`. Exported API keys land there in plaintext | to be filed | upstream merges an equivalent fix |
| P2 | Document required DB privileges; refuse a non-local host unless explicitly allowed | Privileges are documented nowhere (needs CREATEDB + `ALTER DATABASE … ALLOW_CONNECTIONS` + `pg_terminate_backend`, i.e. superuser in practice), the README says a remote server "works", and reclaim drops databases by prefix match | to be filed as an issue first — it changes default behaviour for every user, which is upstream's call, not ours | upstream ships a guard |
| P3 | Meilisearch driver | 7 of 8 FSH stacks run Meilisearch; upstream supports Postgres/MySQL/Mongo/Redis/ES-OpenSearch/S3 and has a clean driver interface to extend | offer once it works for us | upstream merges it |

## Additive files in this fork

| File | Purpose |
|---|---|
| `FORK.md` | this file |
| `.github/workflows/fsh-release.yml` | builds four platforms from source, tests, attests, publishes a release **in this repo** |
| `.github/workflows/fsh-sync.yml` | nightly: fast-forwards `master` from upstream and opens a PR merging it into `fsh` |

## Installing our build

```sh
gh release download --repo fullstackhouse/treeman <tag> --pattern 'treeman-*-darwin-arm64.tar.gz'
gh attestation verify treeman-*-darwin-arm64.tar.gz --repo fullstackhouse/treeman
```

The attestation binds the artifact to this repository, the commit and the workflow that built it.
Consuming repos pin a tag and verify the `sha256` in their own installer script; they do not
`curl | bash` from here.
