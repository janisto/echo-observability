# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The changes in this section target `v2.0.0` under module path
`github.com/janisto/echo-observability/v2`. They must not be tagged on the v1
module path.

### Migration from v1

- Change imports and installation commands to
  `github.com/janisto/echo-observability/v2`.
- Enable `CapturePath`, `CapturePeerIP`, `CaptureUserAgent`, and
  `CaptureError` explicitly where those privacy-sensitive fields are still
  required. Error text can contain application or user data.
- Rename consumers of `remote_ip` to `peer_ip`; the new field uses the direct
  socket peer and ignores proxy-derived addresses.
- Update route dimensions to canonical `{name}` and `{*path}` templates.
- Update error and status queries to use authoritative committed status,
  standardized `service_error`/`panic` terminal reasons, and unconditional
  `ERROR` severity for abnormal completion.
- Treat custom request-ID validators as caller-input narrowing only; generated
  IDs always retain the package baseline grammar.
- Remove v1 compatibility aliases and shims; migrate imports and configuration
  directly to the documented v2 surface.

### Added

- Add GCP profile `0.1.0` selection with newest-supported default resolution,
  exact pinning, and effective-version introspection.
- Add independent `CapturePath`, `CapturePeerIP`, `CaptureUserAgent`, and
  `CaptureError` access-log opt-ins.
- Add immutable W3C Trace Context Level 1/Level 2 selection, effective-level
  resolution, complete selected-level `tracestate` validation, and Level 2
  `trace_id_random` projection.

### Changed

- Renamed the remaining internal direct-peer field so v2 source no longer
  models the removed portable `remote_ip` name. The required GCP
  `httpRequest.remoteIp` output member is unchanged.
- Documented LF-terminated NDJSON as the logging boundary and added focused
  raw-writer coverage for independently parseable records.

- **Breaking:** Omit raw path, direct peer IP, user agent, and returned error
  text from access logs by default. Applications that need them must enable
  the matching options.
- **Breaking:** Rename the opt-in direct-peer field from `remote_ip` to
  `peer_ip`; it now reads only `Request.RemoteAddr` and ignores Echo proxy IP
  extraction. Narrow GCP `httpRequest.requestUrl` to the query-free path.
- Align the GCP health fixture to service version `1.0.0`, operation
  `health_check`, and deterministic 12.5 ms access timing.
- **Breaking:** Reject duplicate raw request-ID and `traceparent` field-lines,
  and prevent custom request-ID validators from admitting values outside the
  package's safe baseline grammar.
- **Breaking:** Remove status inference from errors handled later by Echo, add
  `service_error` and `panic` terminal reasons, and make abnormal records
  unconditionally `ERROR` while preserving returned errors and panics.
- Contain panics from the access clock, status mapper, enrichment callback, and
  writer without changing the response; keep the first repeated custom field
  so package-controlled JSON contains no duplicate member names.
- **Breaking:** Canonicalize Echo `:name` and terminal `*` route metadata to
  portable `{name}` and `{*path}` templates; omit ambiguous native forms.
- Fold every GCP severity into the portable five-level vocabulary, reject
  terminal or unknown status-callback levels, omit unavailable request paths,
  and emit only canonical unzoned IP address literals for direct peers.

### Fixed

- Preserve framework-valid route parameter names, including extended and
  longer names, reject non-ASCII or control-bearing `traceparent` fields, and
  reject trace-level disagreement regardless of middleware order. Reject
  unknown presets consistently and prevent access enrichment from replacing
  Zap-owned caller and Level 2 trace fields.

- Preserve sampling while omitting the Level 2 random flag for unknown future
  `traceparent` versions.
- Lock the exported and default-resolved GCP profile identifier to the literal
  `0.1.0` in native regression tests.

## [1.0.1] - 2026-07-17

### Changed

- Lower the minimum supported Echo v5 version from v5.2.1 to v5.2.0 and add
  CI coverage against the latest Echo v5 release.
- Expand the canonical GCP health example with correlated info, debug, and
  access records containing developer-defined service fields, plus an
  in-process JSON-output test for level filtering and field separation.

## [1.0.0] - 2026-07-16

### Added

- Add project health, Go reference, release, Go version, and license badges to
  the README.
- Add a maintainer release guide and repository-specific contributor guidance
  for validation, security checks, pull requests, and releases.
- Add a `just mutation` recipe backed by the Gremlins CLI, with
  contributor guidance for reviewing meaningful surviving mutants.
- Add a `just fuzz` recipe and contributor guidance for running the existing
  `FuzzParseTraceparent` target with Go's native fuzzing engine.

### Changed

- Stabilize the exported API and documented structured log fields under the
  semantic-versioning compatibility guarantees of the v1 release line.
- Expand the README with the package rationale and its structured-logging and
  request-correlation scope.

## [0.2.0] - 2026-07-15

### Fixed

- Avoid running access-log enrichment callbacks for disabled log levels.
- Preserve the original handler panic if access-log level selection, enrichment,
  or writing also panics while the handler stack is unwinding.

### Changed

- Simplify the basic, GCP, AWS, and Azure examples to focus on package setup and
  one health route, without changing the public API.
- Add a repository-local adversarial-testing skill and strengthen regression
  coverage for boundaries, failure side effects, concurrency, request metadata,
  and W3C trace parsing.

## [0.1.0] - 2026-07-12

### Added

- Add Echo v5 request-context middleware with validated/generated request IDs,
  W3C trace correlation, and standard context propagation.
- Add structured Zap access logging with route templates, explicit route names,
  returned-error status resolution, panic rethrow, and custom fields. Synthetic
  Echo 404/405 route names are excluded, and committed wire statuses are
  preserved when a handler subsequently panics.
- Add standard `net/http` request-context middleware for mixed Echo/router
  services.
- Add generic, Google Cloud, AWS, and Azure logger presets.
- Honor Echo's configured `IPExtractor` and scheme resolution in access logs.
- Combine multiple W3C `tracestate` header fields in wire order.
- Document the `echo.HTTPStatusCoder` contract for custom error mappings and
  keep observability outside fallible application middleware in examples.
- Run CI on the latest patched Go 1.25 toolchain instead of the vulnerable
  initial 1.25.0 release.
- Add runnable cloud examples, a tested local wrapper, public documentation,
  CI, linting, and dependency automation.

[Unreleased]: https://github.com/janisto/echo-observability/compare/v1.0.1...HEAD
[1.0.1]: https://github.com/janisto/echo-observability/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/janisto/echo-observability/compare/v0.2.0...v1.0.0
[0.2.0]: https://github.com/janisto/echo-observability/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/janisto/echo-observability/releases/tag/v0.1.0
