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
- Custom request-ID validators apply only to caller input and may broaden it
  within Go's native HTTP field-value boundary; generated IDs retain the
  package baseline grammar.
- Remove v1 compatibility aliases and shims; migrate imports and configuration
  directly to the documented v2 surface.

### Added

- Added independent `CapturePath`, `CapturePeerIP`, `CaptureUserAgent`, and
  `CaptureError` access-log opt-ins.
- Added immutable W3C Trace Context Level 1/Level 2 selection, effective-level
  resolution, complete selected-level `tracestate` validation, and Level 2
  `trace_id_random` projection.
- Added a conditional consumer-image build as a packaging and integration
  diagnostic, with Podman-first local builds and Docker fallback. Optional
  independent audits are informational and never a publication requirement.

### Changed

- Applied the RFC 9110 field-content and valid UTF-8 boundary before custom
  request-ID validation.
- Defined LF-terminated NDJSON as the logging boundary.
- Omitted invalid UTF-8 User-Agent field values before Zap encoding could
  replace their bytes; preserved framework-accepted Unicode and internal
  whitespace.
- Omitted raw path, direct peer IP, user agent, and returned error
  text from access logs by default. Applications that need them must enable
  the matching options.
- Renamed the opt-in direct-peer field from `remote_ip` to
  `peer_ip`; it now reads only `Request.RemoteAddr` and ignores Echo proxy IP
  extraction. Narrowed GCP `httpRequest.requestUrl` to the query-free path.
- Rejected duplicate raw request-ID and `traceparent` field-lines;
  custom request-ID validators can broaden caller input within Go's native HTTP
  field-value boundary while generated IDs remain strict.
- Removed status inference from errors handled later by Echo, added
  `service_error` and `panic` terminal reasons, and made abnormal records
  unconditionally `ERROR` while preserving returned errors and panics.
- Contained panics from the access clock, status mapper, enrichment callback,
  and writer without changing the response.
- Kept the first repeated custom field so package-controlled JSON contains no
  duplicate member names.
- Canonicalized simple Echo `:name` and terminal `*` route metadata
  to portable `{name}` and `{*path}` templates; preserved richer nonempty
  matched templates in Echo's authoritative native form.
- Folded every GCP severity into the portable five-level vocabulary, rejected
  terminal or unknown status-callback levels, omitted unavailable request
  paths, and emitted only canonical unzoned IP address literals for direct
  peers.

### Removed

- Removed v1 compatibility aliases and configuration shims from the v2 API.

### Fixed

- Protected only exact record-owned top-level fields in raw NDJSON, while
  preserving access-only application fields, exact aliases owned only by an
  inactive provider preset, other non-owned provider-looking keys, application
  namespaces, and reserved-looking fields nested with `zap.Namespace`.
- Preserved the selected provider preset through `HTTPRequestContext`, rejected
  a mismatched preset whenever existing request metadata is reused, and called
  a configured request-ID generator once before using the package fallback.
- Preserved framework-exposed escaped request paths, including asterisk-form
  paths, without package-invented percent-encoding validation.
- Kept the default request-ID entropy path on successful reads and used the
  package fallback only on read failure.
- Emitted GCP `httpRequest.latency` with canonical ProtoJSON fractional widths:
  0, 3, 6, or 9 digits according to the required precision.
- Preserved framework-valid route parameter names, including colon-bearing and
  longer names, HTTP-safe opaque future `traceparent` suffixes without an
  invented length cap, valid `tracestate` beyond 512 characters, HTAB
  User-Agent values, custom-admitted request IDs, and nonempty static operation
  IDs. Rejected provider-preset or trace-level disagreement regardless of
  middleware order, rejected unknown presets consistently, and protected
  Zap-owned caller and Level 2 trace fields.
- Admitted a comma in one request-ID field-line when the configured
  application validator accepts it; real duplicate field-lines remain
  rejected.
- Installed recovery outside access logging in Echo's middleware chain so panic
  access records retain `terminal_reason: "panic"` before recovery creates the
  application response.
- Preserved sampling while omitting the Level 2 random flag for unknown future
  `traceparent` versions.
- Rejected `zap.Inline` values from access-log `ExtraFields` so nested
  marshalers cannot bypass reserved-key collision protection.

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
