# Changelog

## Unreleased

- Add Echo v5 request-context middleware with validated/generated request IDs,
  W3C trace correlation, and standard context propagation.
- Add structured Zap access logging with route templates, explicit route names,
  returned-error status resolution, panic rethrow, and custom fields.
- Add standard `net/http` request-context middleware for mixed Echo/router services.
- Add generic, Google Cloud, AWS, and Azure logger presets.
- Honor Echo's configured `IPExtractor` and scheme resolution in access logs.
- Combine multiple W3C `tracestate` header fields in wire order.
- Document the `echo.HTTPStatusCoder` contract for custom error mappings and
  keep observability outside fallible application middleware in examples.
- Run CI on the latest patched Go 1.25 toolchain instead of the vulnerable
  initial 1.25.0 release.
- Add runnable cloud examples, a tested local wrapper, public documentation,
  CI, linting, and dependency automation.
