# Changelog

All notable changes to MCPShim are documented here, following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.0.3] - 2026-07-24

### Added

- Expose configured HTTP services as typed tools or constrained curl-like raw
  request tools through the existing discovery, call, wrapper, and history
  interfaces.
- Add recursive structural JSON templates with typed argument injection,
  defaults, optional values, and scalar string formatting.
- Add per-service HTTP policies for methods, paths, headers, redirects, content
  types, successful statuses, request and response sizes, timeouts, and returned
  response headers.
- Add `server.allow_registry_writes` for deployments that want socket clients to
  keep changing the registry at runtime.

### Changed

- Refuse `add_server`, `set_auth`, and `remove_server` from socket clients
  unless `server.allow_registry_writes` is enabled, because those actions
  rewrite the configuration file and would otherwise make socket access
  equivalent to configuration write access. Edit the configuration file and
  reload instead, or opt in. Reloading is unaffected: it only re-reads what is
  already on disk.

### Fixed

- Withhold the response body when its content type is not in
  `allowed_response_content_types`. The status, content type, and allowlisted
  headers are still returned so failures stay diagnosable.

## [0.0.2] - 2026-07-24

### Added

- Add versioned release metadata, automated tagging, changelog-backed release
  notes, and multi-platform container publication.
- Add focused tests for configuration, SQLite persistence, server mutations,
  structured CLI arguments, and MCP tool errors.
- Add a non-root container image with persistent config and data volumes.
- Add bounded call-history retention and safe scoped or explicit all-history
  clearing.

### Changed

- Use a pure-Go SQLite driver so static release binaries work on every
  advertised platform.
- Preserve environment-variable references when editing configuration instead
  of writing expanded credentials back to disk.
- Reject configuration that references an unset environment variable instead
  of attempting authentication with an empty value.
- Accept JSON objects, arrays, and null values in dynamic tool arguments.

### Fixed

- Create the database with mode `0600` because it stores OAuth access and
  refresh tokens.
- Create a missing socket parent directory with mode `0700`.
- Keep the running configuration unchanged when validation or persistence of a
  registry mutation fails.
- Bind OAuth tokens to both server name and endpoint, preventing a renamed
  endpoint from receiving a token issued for the old URL. Existing OAuth
  servers require one authorization refresh after upgrading.
- Reject unsupported transports instead of silently changing them to HTTP.
- Report MCP results marked `isError` as failed calls while preserving their
  content in JSON output and call history status.
- Return an actionable login error instead of opening an OAuth browser flow
  from inside the daemon.

## [0.0.1] - 2026-02-23

### Added

- Initial daemon, CLI, MCP transports, OAuth flow, configuration, wrapper
  generation, and SQLite call history.
