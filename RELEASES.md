# Releasing MCPShim

Releases are driven by the `VERSION` file:

1. Update `VERSION` and add a matching section to `CHANGELOG.md`.
2. Merge the change to `main`.
3. CI runs race-enabled and static release-mode tests.
4. After CI succeeds, `tag-release.yaml` creates an annotated `v*` tag at the
   tested commit and dispatches `release.yaml`.
5. The release workflow validates, rebuilds, packages, checksums, and publishes
   binaries and a matching container image.

Existing tags and releases are never changed.

## Version embedding

Both binaries embed the release tag through Go linker flags. Development builds
default to `dev`.

## Published platforms

| OS      | Architecture |
| ------- | ------------ |
| Linux   | amd64, arm64 |
| macOS   | amd64, arm64 |
| Windows | amd64        |

Container images are published for Linux amd64 and arm64 at
`ghcr.io/mcpshim/mcpshim`. Stable releases update `latest`; prereleases publish
only versioned tags.

## Local release checks

```bash
make test
CGO_ENABLED=0 go test ./... -count=1
make build VERSION=v0.0.3
./mcpshim version
./mcpshimd --version
docker build --build-arg VERSION=v0.0.3 --tag mcpshim/mcpshim:local .
```

Keep `VERSION` bare, without a leading `v`, and use semantic versioning.
