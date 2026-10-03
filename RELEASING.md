# Releasing

Releases are **tag-triggered** — pushing to `main` alone does not produce
any artifact. Pushing a `v*.*.*` tag to the public repository
(`DayMug/DayMug`) runs `.github/workflows/release.yml` on a GitHub-hosted
`ubuntu-latest` runner, which builds **linux/amd64**, **linux/arm64** and
**darwin/arm64** and publishes them as a GitHub Release. Nothing upgrades
automatically: an install only moves when an admin clicks Update or runs
`daymug upgrade`.

Every release is kept forever: GitHub Releases are never pruned, so any
version stays installable (`install.sh vX.Y.Z`, `daymug upgrade --version`).

## Defaults — don't deviate unless you were asked to

- **Every platform, every release.** A plain tag push builds linux/amd64,
  linux/arm64 and darwin/arm64; there is no opt-in or opt-out.
- **Bump the third digit.** `v1.2.45 → v1.2.46`. A minor (`v1.3.0`) or major
  bump needs an explicit instruction — never infer one from the size of the
  diff or from what landed in `FEATURES.md`. See "Versioning" below.

## Cutting a binary release

```bash
# Pick the next version. Latest tag:
git tag -l 'v*' --sort=-v:refname | head -1

# Tag from main and push. Third digit only.
git tag v1.2.46
git push origin v1.2.46
```

The job:

1. Builds the frontend once (`pnpm build`, with `APP_VERSION=<tag>`).
2. Cross-compiles the Go backend with `embed_frontend`, pure-Go SQLite, and
   `-ldflags "-s -w -X main.Version=<tag> -X …/service.DefaultManifestURL=https://github.com/<repo>/releases/latest/download/manifest.json"`
   (JS/CSS are gzip-precompressed before embedding), so a fork's binaries
   self-upgrade from the fork. Targets: linux/amd64 (UPX-packed),
   linux/arm64 (not packed: UPX stubs are unreliable on the 16K/64K-page
   kernels common on arm64) and darwin/arm64 (never UPX-packed because that
   breaks code signing).
3. Code-signs and notarizes the macOS binary via Anchore `quill` — skipped
   if the `QUILL_SIGN_P12` secret is unset.
4. Writes `checksums.txt` (`sha256sum -c`-compatible, computed after
   signing) and `manifest.json`: the version, each binary's tag download URL
   + sha256, and `recent_releases` for the upgrade panel's notes
   (`scripts/recent_releases.py`): the 5 newest tags plus the newest tag of
   each of the 5 newest minor lines — e.g. v1.5.5…v1.5.1, v1.4.x, v1.3.x,
   v1.2.x, v1.1.x — each with its commit subjects minus
   `chore:`/`docs:`/`ci:`.
5. Publishes the GitHub Release with the three binaries, `checksums.txt`,
   `manifest.json`, `install.sh` and `config.example.yaml`, and marks it
   latest. Re-running a tag replaces the assets of the existing release
   (`gh release upload --clobber`) instead of failing.

What reads the release:

| URL | Reader |
|-----|--------|
| `releases/latest/download/manifest.json` | self-upgrade (`service.DefaultManifestURL`), `install.sh` |
| `releases/download/<tag>/manifest.json` | `daymug upgrade --version <tag>`, `install.sh <tag>` |
| `releases/latest/download/install.sh` | `curl -fsSL … \| bash` |

Once the release is published, admins can click **Settings → Admin → Global
→ Check for updates** to roll forward without an SSH session.

### Required repo configuration

Nothing is required: the job publishes with its own `github.token`
(`contents: write`). Optional settings live under **Settings → Secrets and
variables → Actions**.

**Secrets** (macOS signing; all skipped when unset):
- `QUILL_SIGN_P12` — base64 of the Apple Developer ID P12
- `QUILL_SIGN_PASSWORD` — P12 password
- `QUILL_NOTARY_KEY` — base64 of the App Store Connect `.p8` (notarization
  on top of signing)
- `QUILL_NOTARY_KEY_ID`
- `QUILL_NOTARY_ISSUER`

## Re-running and rollback

- **Re-run a build for an existing tag**: GitHub Actions UI → workflow →
  "Run workflow" → enter the tag in the `tag` input.
- **Self-upgrade rollback**: `daymug upgrade --rollback` on the host, or
  the **Rollback** button in the same Admin → Global panel. Restores
  `<bin>.bak` and the saved version.
- **Pull a bad release**: mark the previous release as *Latest* in the
  GitHub UI (or `gh release edit <prev-tag> --latest`); the rolling
  `releases/latest/download/` URLs follow it immediately.

## Versioning

**Bump the third digit** (`v1.2.45 → v1.2.46`). That is the default for
everything — fixes, UX tweaks, docs, and features alike.

Minor (`v1.2.x → v1.3.0`) or major bumps happen only when someone explicitly
asks. Shipping something that shows up in `FEATURES.md` is *not* on its own a
reason to bump the minor: ask, then follow the answer.

## Manual verification before tagging

```bash
# Backend
( cd backend && go build ./... && go test ./... && golangci-lint run )

# Frontend
( cd frontend && pnpm build && pnpm test && pnpm lint && pnpm api-contract:check )
```

Pushes to `main` trigger no workflow. A release tag starts `ci.yml` and
`release.yml` together, and `release.yml` does not wait for CI — so run the
pipelines locally (or dispatch `ci.yml` by hand on `main`) before tagging.
