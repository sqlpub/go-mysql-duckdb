# Versioning

This project uses **Semantic Versioning** tags: `vMAJOR.MINOR.PATCH`.

GitHub Releases and Linux packages (`make release-linux`) must use the same tag. The binary embeds version via `-ldflags -X main.version=$(VERSION)` (`VERSION` defaults to `git describe`).

## When to bump

| Bump | Example | Use when |
|------|---------|----------|
| **PATCH** | `v0.2.0` → `v0.2.1` | Bug fixes, DECIMAL/JSON fixes, docs, packaging only. No intentional behavior change for callers. |
| **MINOR** | `v0.2.0` → `v0.3.0` | New features (new HTTP endpoints, config keys, sync capabilities) that stay backward compatible when possible. |
| **MAJOR** | `v0.x` → `v1.0.0` | Public API / config breaking changes, or declaring a stable production line. |

While still on **`0.x`**, MINOR may include breaking changes if needed; document them in the release notes. After **`1.0.0`**, breaking changes require a MAJOR bump.

## Release checklist

1. Land changes on `main`.
2. Choose the next tag from the table above (do not reuse an existing tag).
3. Tag and push:

   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin main
   git push origin vX.Y.Z
   ```

4. Build artifacts:

   ```bash
   VERSION=vX.Y.Z make release-linux
   # optional: make release-linux-arm64 / release-linux-all
   ```

5. Publish GitHub Release for that tag and attach `dist/go-mysql-duckdb-vX.Y.Z-linux-*.tar.gz`.

If the release already exists for the tag, upload/replace assets with `gh release upload … --clobber` instead of creating a duplicate.
