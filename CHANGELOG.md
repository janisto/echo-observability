# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
