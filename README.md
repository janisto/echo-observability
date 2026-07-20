# echo-observability

[![Latest release](https://img.shields.io/github/v/release/janisto/echo-observability)](https://github.com/janisto/echo-observability/releases/latest)
[![Go Reference](https://img.shields.io/badge/go.dev-reference-007d9c?logo=go&logoColor=white)](https://pkg.go.dev/github.com/janisto/echo-observability/v2)
[![Go version](https://img.shields.io/github/go-mod/go-version/janisto/echo-observability)](https://github.com/janisto/echo-observability/blob/main/go.mod)
[![CI](https://img.shields.io/github/actions/workflow/status/janisto/echo-observability/ci.yml?branch=main&label=CI)](https://github.com/janisto/echo-observability/actions/workflows/ci.yml)
[![Socket Badge](https://badge.socket.dev/go/package/github.com/janisto/echo-observability/v2)](https://socket.dev/go/package/github.com/janisto/echo-observability/v2)

`echo-observability` provides request correlation, request-scoped Zap loggers,
and structured Zap access logging middleware for
[Labstack Echo v5](https://github.com/labstack/echo).

## Why this package exists

Managed platforms such as Cloud Run already collect container output.
Applications should only need to write structured JSON to standard output
(`stdout`); the platform can handle ingestion and delivery.

Compared with sending logs through an in-process cloud logging client, this
reduces container CPU, memory, and network use by removing logging API calls,
authentication, buffering, batching, and retry work from the application. Under
sustained logging load, that reduction can provide a noticeable performance
improvement. It also avoids the dependency and maintenance cost of a cloud
logging SDK, including its configuration, credentials, and upgrades.

This package turns that simple pipeline into useful production observability.
It provides validated request IDs, strict W3C trace correlation,
request-scoped fields, and one structured terminal access record. Application
and access logs share the same correlation metadata, making all records from a
request easier to find, filter, and understand.

Cloud presets map the same logging contract to provider-oriented fields without
coupling application code to a cloud logging SDK. The package focuses on
structured logging and request correlation: it does not create spans, configure
OpenTelemetry, or ship logs to a backend.

## Package scope

The module path is `github.com/janisto/echo-observability/v2`; the declared Go
package name is `obs`.

This is not official Echo middleware. It is a small, opinionated package for
services that want a consistent production logging contract on Echo v5.

## When To Use It

Use this package when an Echo v5 service needs:

- Validated or generated request IDs with response propagation.
- Request-scoped `*zap.Logger` values through `obs.Logger(ctx)`.
- Strict W3C `traceparent` parsing and trace-level log correlation.
- One structured access log after each Echo request.
- Low-cardinality `path_template` values from Echo's `c.Path()`.
- Status authority only for responses committed before this middleware
  boundary returns; centralized error-handler statuses are not guessed.
- Generic, Google Cloud, AWS, and Azure JSON field presets.
- Panic access logging followed by re-panic for the application's recovery middleware.
- Router-wide request metadata for health checks, readiness probes, redirects,
  static handlers, 404/405 handlers, and recovery middleware.

This package also does not create metrics, Prometheus endpoints, or separate
endpoint exporters.

## Requirements

- Go 1.25 or newer; deploy with the latest available patch release.
- Echo v5.2.0 or newer within the Echo v5 line.
- Zap.

The v1 API and log contract remains available at the unsuffixed module path.
This checkout targets v2 because its privacy defaults and structured output are
intentionally incompatible with v1. See the changelog migration section before
upgrading.
Version 2 provides no v1 field aliases, option shims, or unsuffixed import
fallback; applications must migrate to the documented v2 API and module path.

## Install

```sh
go get github.com/janisto/echo-observability/v2
```

## Quick Start

When this documentation shows one configuration, it uses GCP. Complete
runnable GCP, provider-neutral, AWS, and Azure applications are available in
[`examples`](examples).

```go
package main

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/janisto/echo-observability/v2"
)

func main() {
	profileVersion, err := obs.ResolveGCPProfileVersion(obs.PresetGCP, "")
	if err != nil {
		panic(err)
	}
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset:            obs.PresetGCP,
		GCPProfileVersion: profileVersion,
		Level:             zapcore.DebugLevel,
	})
	if err != nil {
		panic(err)
	}

	e := echo.New()
	e.Use(
		obs.RequestContext(obs.RequestContextConfig{Logger: logger, Preset: obs.PresetGCP}),
		obs.AccessLogger(obs.AccessLoggerConfig{
			Logger:            logger,
			Preset:            obs.PresetGCP,
			GCPProfileVersion: profileVersion,
		}),
		middleware.Recover(),
	)

	_, err = e.AddRoute(echo.Route{
		Method:  http.MethodGet,
		Path:    "/health",
		Name:    "health_check",
		Handler: func(c *echo.Context) error {
			logger := obs.Logger(c.Request().Context())
			logger.Info("health check",
				zap.String("service_name", "example-service"),
				zap.String("service_version", "1.0.0"),
				zap.String("health_status", "ok"),
			)
			logger.Debug("dependency check",
				zap.String("dependency", "database"),
				zap.String("dependency_status", "ok"),
				zap.Int64("check_duration_ms", 3),
			)
			return c.JSON(http.StatusOK, map[string]bool{"ok": true})
		},
	})
	if err != nil {
		panic(err)
	}

	if err := e.Start(":8080"); err != nil {
		logger.Error("server stopped", zap.Error(err))
	}
}
```

Install `RequestContext` before `AccessLogger`. Put recovery middleware after
`AccessLogger` when panics must produce an access log before being recovered.
Echo applies `Use` middleware after routing, so `c.Path()` contains the matched
route template. `NewLogger` defaults to info level; this example enables debug
to show that both application levels retain the same request correlation fields
as the terminal access record.

## Middleware

### RequestContext

`RequestContext` validates the incoming request ID, generates a safe ID when
needed, parses W3C trace context, installs metadata on
`c.Request().Context()`, and adds the request ID response header.

```go
e.Use(obs.RequestContext(obs.RequestContextConfig{
	Logger:            logger,
	Preset:            obs.PresetGCP,
	TraceContextLevel: obs.TraceContextLevel1,
}))
```

Defaults:

| Setting | Default |
| --- | --- |
| Request ID header | `X-Request-Id` |
| Response header | Request ID header |
| Trace header | `traceparent` |
| Trace state header | `tracestate` |
| Trace Context level | W3C Level 1 |
| Request ID format | 32 lowercase hexadecimal characters |

Incoming request IDs are at most 128 bytes and may contain ASCII letters,
digits, `-`, `.`, `_`, and `~`. A custom `ValidateRequestID` may further
restrict that baseline but cannot admit an unsafe value. Multiple raw request
ID field-lines are ambiguous and cause a replacement ID to be generated.
Set `DisableResponseHeader` when the request ID must not be returned.

Access metadata anywhere a standard `context.Context` is available:

```go
ctx := c.Request().Context()
requestID := obs.RequestID(ctx)
correlationID := obs.CorrelationID(ctx)
trace := obs.Trace(ctx)
logger := obs.Logger(ctx)
```

`Logger` always returns a non-nil logger. It returns a no-op logger outside an
installed request context.

### HTTPRequestContext

For services with both Echo and non-Echo routes, install
`HTTPRequestContext` at the outer `net/http` boundary:

```go
mux := http.NewServeMux()
mux.Handle("/", e)
mux.HandleFunc("GET /ready", readyHandler)

handler := obs.HTTPRequestContext(obs.HTTPRequestContextConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
})(mux)
```

`HTTPRequestContext` installs request IDs, trace correlation metadata,
response request ID headers, and an optional request-scoped logger for every
HTTP request. Echo `RequestContext` reuses that metadata, so an inbound request
keeps one request ID across both layers.

It does not emit access logs or wrap `http.ResponseWriter`. Echo routes should
use `AccessLogger`; access logging for non-Echo routes remains
application-owned. The same header, generation, validation, response-header,
logger, and preset options are available in `HTTPRequestContextConfig`.

## Handler Logging

Use `obs.Logger(ctx)` anywhere a standard request context is available:

```go
func loadRepository(ctx context.Context, owner, repo string) error {
	obs.Logger(ctx).Info("loading repository",
		zap.String("owner", owner),
		zap.String("repo", repo),
	)
	return nil
}
```

Configure `RequestContextConfig.Logger` for Echo-only services, or
`HTTPRequestContextConfig.Logger` at the router boundary for mixed services.
Background jobs, scripts, direct service calls, and tests using
`context.Background()` must use an explicit process logger; `obs.Logger(ctx)`
is intentionally a no-op outside installed request metadata.

## Access Logger

`AccessLogger` installs request metadata by itself when `RequestContext` is
missing, but explicit installation of both middlewares is preferred. It emits:

- `method`
- `path` — escaped request path when `CapturePath` is enabled; never includes
  the query string
- `path_template` — canonical Echo route pattern such as `/users/{id}` or `/files/{*path}`
- `operation_id` — explicit Echo route name, when configured
- `status` — only when the response was already committed at this boundary
- `duration_ms`
- `terminal_reason` — `service_error` for returned errors or `panic`
- `peer_ip` — direct `Request.RemoteAddr` peer when `CapturePeerIP` is enabled
- `user_agent` when `CaptureUserAgent` is enabled
- `error` — returned Echo error only with the privacy-sensitive `CaptureError` opt-in

The request-scoped fields are `request_id`, `correlation_id`, and, for valid
W3C trace context, `trace_id`, `parent_id`, `trace_flags`, and
`trace_sampled`. Explicit Level 2 also adds `trace_id_random` for version `00`.

Only a response committed before the handler returns supplies logged status.
A returned error uses terminal reason `service_error`, level `ERROR`, and no
status when Echo's centralized error handler has not run yet. The original
error is returned unchanged for that handler. `AccessLogger` intentionally
does not invoke the global error handler itself because that would commit the
response inside logging middleware. Consequently, the later wire status may be
absent from this package's record rather than guessed from
`echo.HTTPStatusCoder`.

Use `ExtraFields` for application-owned access-log fields. Package-owned and
provider-owned field names are ignored to prevent duplicate JSON keys.
If the returned Zap field slice repeats a custom key, the first value wins.
That collision guarantee applies to package-controlled context and access
merges. Arbitrary fields passed directly to a raw Zap logger are application
owned; callers must not reuse package-reserved names or emit duplicate keys.
`ExtraFields` is evaluated only when the selected access-log level is enabled,
so suppressed logs do not run application enrichment callbacks.

`CapturePath`, `CapturePeerIP`, `CaptureUserAgent`, and `CaptureError` are
independent and default to false. A provider preset never enables them. Rich
error messages can contain secrets and require an explicit privacy decision. `peer_ip` ignores
forwarded headers and Echo's `IPExtractor`; proxy-derived client identity is a
different, application-owned concept.

```go
e.Use(obs.AccessLogger(obs.AccessLoggerConfig{
	Logger:            logger,
	Preset:            obs.PresetGCP,
	GCPProfileVersion: profileVersion,
	ExtraFields: func(c *echo.Context) []zap.Field {
		return []zap.Field{zap.String("tenant_id", tenantID(c))}
	},
}))
```

`StatusLevel` can override the normal-response mapping: 5xx is error, 4xx is
warn, and all other statuses are info. Abnormal terminal reasons always use
error. `Now` exists for deterministic testing.

## Named Routes

Echo assigns an internal default route name. `operation_id` is emitted only
for an explicitly named route:

```go
_, err := e.AddRoute(echo.Route{
	Method:  http.MethodGet,
	Path:    "/users/:id",
	Name:    "get-user",
	Handler: getUser,
})
```

With `CapturePath` enabled, the raw request `/users/123` logs `path=/users/123` and
`path_template=/users/{id}`. Echo whole-segment `:name` parameters become
`{name}`, and its unnamed terminal `*` becomes `{*path}`. Ambiguous optional or
composite forms are omitted. Group metrics or logs by `path_template`, not
`path`, to avoid high-cardinality dimensions.

## Trace Correlation

W3C `traceparent` is the only trace input. A valid trace ID becomes
`correlation_id`; otherwise `correlation_id` falls back to `request_id`.
Level 1 is the default. Select the pinned Level 2 mode explicitly and use the
same immutable level for request context and access logging:

```go
const traceLevel = obs.TraceContextLevel2
e.Use(
	obs.RequestContext(obs.RequestContextConfig{
		Logger: logger, Preset: obs.PresetGCP, TraceContextLevel: traceLevel,
	}),
	obs.AccessLogger(obs.AccessLoggerConfig{
		Logger: logger, Preset: obs.PresetGCP, TraceContextLevel: traceLevel,
	}),
)
```

`ResolveTraceContextLevel(0)` exposes the effective default. Unsupported
levels fail during middleware construction. Exactly one raw `traceparent`
field-line is eligible. Multiple `tracestate` fields are combined in wire
order and validated with the selected level's complete key/value grammar,
unique keys, at most 32 members, and a 512-byte raw ceiling. Invalid
`tracestate` is discarded without discarding a valid `traceparent`.
For version `00`, Level 2 projects bit one of `trace_flags` as
`trace_id_random`. Level 1 and unknown higher versions preserve the
two-character flags but do not assign that bit portable meaning.

Provider-specific headers such as `X-Cloud-Trace-Context`,
`X-Amzn-Trace-Id`, and Azure's legacy `Request-Id` are intentionally not
parsed. The package correlates logs; it does not create spans or provider trace
segments.

## Cloud Presets

Use the same preset for `NewLogger`, `RequestContext`, and `AccessLogger`.

```go
profileVersion, err := obs.ResolveGCPProfileVersion(obs.PresetGCP, "")
if err != nil {
	return err
}
logger, err := obs.NewLogger(obs.LoggerConfig{
	Preset:            obs.PresetGCP,
	GCPProfileVersion: profileVersion,
})
if err != nil {
	return err
}
e.Use(
	obs.RequestContext(obs.RequestContextConfig{
		Logger: logger, Preset: obs.PresetGCP,
	}),
	obs.AccessLogger(obs.AccessLoggerConfig{
		Logger: logger, Preset: obs.PresetGCP, GCPProfileVersion: profileVersion,
	}),
)
```

### Google Cloud

The GCP preset emits `severity` instead of `level`, a structured
`httpRequest` object on access lines, `logging.googleapis.com/trace`, and
`logging.googleapis.com/trace_sampled`. The trace field contains the raw W3C
trace ID, which is Google Cloud's preferred format. It deliberately does not
emit `logging.googleapis.com/spanId` from the incoming parent ID.

The installed package supports GCP profile `0.1.0`. Omitting the version
selects that newest supported version during construction; exact pinning uses
`GCPProfileVersionV0_1_0`. `ResolveGCPProfileVersion` exposes the effective
value without a provider, registry, or network lookup. Invalid selections make
`NewLogger` return an error and make `AccessLogger` panic immediately during
middleware construction because its established API has no error return.

GCP `httpRequest.requestUrl` is the exact captured path only, never scheme,
authority, query, or fragment. `remoteIp` and `userAgent` appear only when the
corresponding portable privacy option is enabled.

Captured paths and peers are validated rather than repaired: unavailable or
non-origin-form paths are omitted, and peer fields contain only canonical
unzoned IPv4 or IPv6 address literals. GCP severities always use `DEBUG`,
`INFO`, `WARNING`, `ERROR`, or `CRITICAL`. A custom status mapper returning a
terminal or unknown Zap level falls back to the default status mapping.

### AWS

The AWS preset keeps flat `timestamp`, `level`, and `message` fields. A valid
W3C trace also emits `xray_trace_id` in `1-8hex-24hex` form. It does not create
X-Ray segments or treat the incoming parent ID as a current X-Ray span.

### Azure

The Azure preset keeps flat JSON and maps a valid W3C trace to
`operation_Id` and `operation_ParentId`. It does not initialize Application
Insights or create dependency/request telemetry.

An incoming W3C parent ID is not emitted as a current span ID. A current span
ID can only come from real tracing instrumentation.

## Field Contract

Every JSON line created by `NewLogger` uses:

- `timestamp`: UTC RFC3339 with nanosecond precision.
- `level`, or `severity` for GCP.
- `logger`: present for named Zap loggers.
- `message`.

Request-scoped lines add:

- `request_id`.
- `correlation_id`.
- `trace_id`, `parent_id`, `trace_flags`, and `trace_sampled` only for a valid
  W3C trace; `trace_id_random` additionally for version `00` in explicit Level
  2 mode.
- Provider-specific trace fields selected by the configured preset.

Access lines add:

- `method`.
- `path`: escaped concrete URL path without the query string, when opted in.
- `path_template`: parameterized Echo route path when matched.
- `operation_id`: explicitly configured Echo route name.
- `status`, only when committed before the middleware boundary returns.
- `duration_ms`.
- `terminal_reason` for returned errors and panics.
- `peer_ip`: direct transport peer from `Request.RemoteAddr`, when opted in.
- `user_agent` when opted in and present.
- `error` when Echo middleware or the handler returns an error and `CaptureError` is enabled.
- `httpRequest` for the GCP preset only.

`ExtraFields` applies only to the access line. Reserved package and provider
keys are ignored so custom fields cannot produce duplicate or forged owned
values.

## Request IDs

The default generator reads 128 bits from `crypto/rand` and encodes them as 32
lowercase hexadecimal characters. If entropy acquisition fails, or a custom
generator returns invalid data twice, a process-local atomic fallback is used.

The default validator accepts 1–128 ASCII characters from the unreserved URI
set: letters, digits, `-`, `.`, `_`, and `~`. A custom validator can only
narrow caller input and is never applied to generated or package-fallback IDs.
The configured generator is tried exactly twice unless its first result passes
the baseline. Validator and generator panics are contained as rejection or
failure and do not bypass the handler. Invalid client input is replaced, never
copied to response headers or logs.

## Middleware Placement

Install request context and access logging at the outer observability boundary
so downstream middleware failures are correlated and logged:

```go
e.Use(
	obs.RequestContext(obs.RequestContextConfig{
		Logger: logger,
		Preset: obs.PresetGCP,
	}),
	obs.AccessLogger(obs.AccessLoggerConfig{
		Logger: logger,
		Preset: obs.PresetGCP,
		GCPProfileVersion: profileVersion,
	}),
	middleware.CORS(),
	middleware.BodyLimit(1<<20),
	middleware.Recover(),
)
```

Read request metadata with `obs.RequestID(c.Request().Context())` and log with
`obs.Logger(c.Request().Context())`. Echo's own `e.Logger` remains separate
from application request logging.

Configure `e.IPExtractor` for application features that need proxy-derived
client identity. `AccessLogger` deliberately does not use it for portable
`peer_ip`; that field is direct transport metadata and ignores forwarded
headers.

Keep the observability pair outside middleware such as `BodyLimit`, CORS, and
authentication when their rejected requests must also receive request IDs and
access logs.

## Logger Configuration

`NewLogger` writes JSON application logs to stdout and Zap internal errors to
stderr by default. `LoggerConfig` supports `Level`, `Writer`, `ErrorWriter`,
`AddCaller`, and `Development`. Add stable application fields to the returned
base logger before passing it to middleware:

```go
logger = logger.With(
	zap.String("service", "example-api"),
	zap.String("environment", "production"),
	zap.String("version", version),
)
```

Do not log authorization headers, cookies, tokens, request bodies, or other
secrets and personal data.

## Panic Behavior

`AccessLogger` recovers a panic only long enough to emit an `ERROR` access log
with terminal reason `panic`, then re-panics with the original value. An
uncommitted response has no logged status. If the response was already
committed, its wire status is preserved in the log.
If access-log enrichment or writing also panics while the handler panic is
unwinding, the original handler panic remains the value propagated downstream.
On a normal handler path, a panicking clock, status mapper, enrichment callback,
or access writer is contained: safe defaults are used when possible and the
HTTP response is unchanged. Failed writer calls are not retried.
Install the application's recovery
middleware inside it—later in the `e.Use` list—when the application must turn
panics into HTTP responses. The package never swallows a downstream handler
panic or owns the response format.

## Optional Local Wrapper

Projects that prefer application-specific helpers can wrap the context API
without introducing another logging backend. A complete tested example is in
[`examples/local-wrapper/applog`](examples/local-wrapper/applog).

```go
func Info(ctx context.Context, msg string, fields ...zap.Field) {
	obs.Logger(ctx).Info(msg, fields...)
}
```

Keep the wrapper local to the application. This package intentionally exposes
Zap directly rather than defining a second logger interface.

## Validation

Development uses [just](https://github.com/casey/just). On macOS, install the
workflow linters:

```sh
brew install actionlint zizmor
```

Then run the repository gates:

```sh
just install
just qa
just vuln
```

`just qa` validates the Go 1.25 support line with formatting, lint, build,
tests, race tests, [actionlint](https://github.com/rhysd/actionlint), and
[zizmor](https://docs.zizmor.sh/). `just vuln` runs the Go vulnerability scanner
separately.

The suite covers the real Echo adapter path, standard `net/http` composition,
request ID and trace boundaries, returned and committed response errors,
panic rethrow, concurrent logging, cloud field contracts, reserved fields,
and request-context immutability. `ParseTraceparent` also has a fuzz target.

## References

- [Echo v5](https://github.com/labstack/echo)
- [Echo middleware](https://echo.labstack.com/docs/category/middleware)
- [Echo error handling](https://echo.labstack.com/guide/error-handling/)
- [Go 1.25 release notes](https://go.dev/doc/go1.25)
- [W3C Trace Context](https://www.w3.org/TR/trace-context/)
- [W3C Trace Context Level 2](https://www.w3.org/TR/2024/CRD-trace-context-2-20240328/)
- [Zap](https://github.com/uber-go/zap)
- [Google Cloud structured logging](https://cloud.google.com/logging/docs/structured-logging)
- [Google Cloud trace and log integration](https://docs.cloud.google.com/trace/docs/trace-log-integration)
- [Google Cloud Trace release notes](https://docs.cloud.google.com/trace/docs/release-notes)
- [AWS X-Ray trace header](https://docs.aws.amazon.com/xray/latest/devguide/xray-concepts.html#xray-concepts-tracingheader)
- [Azure Application Insights data model](https://learn.microsoft.com/azure/azure-monitor/app/data-model-complete)

## License

MIT. See [LICENSE](LICENSE).

## Mutation Testing

Install [Gremlins](https://github.com/go-gremlins/gremlins) with Homebrew on
macOS:

```sh
brew tap go-gremlins/tap
brew install gremlins
```

Then run its mutation campaign against covered production code with:

```sh
just mutation
```

Gremlins changes expressions and conditions, then checks whether the existing
tests detect each behavioral change. Review `LIVED` mutants as possible test
gaps; equivalent transformations do not need artificial assertions. Mutation
testing intentionally runs outside `just qa` and may take several minutes. The
configured per-mutant safety timeout does not limit the total campaign time.

## Fuzz Testing

This repository uses Go's native fuzzing engine for `FuzzParseTraceparent`.
Run the default ten-second session with:

```sh
just fuzz
```

Pass the target and duration explicitly for a longer run:

```sh
just fuzz FuzzParseTraceparent 1m
```

The equivalent native Go command is:

```sh
go test -fuzz=FuzzParseTraceparent -fuzztime=10s .
```

Go first replays the seed corpus and then generates new inputs. When fuzzing
finds a failure, it minimizes the input and writes it under
`testdata/fuzz/FuzzParseTraceparent`; normal `go test ./...` runs saved corpus
inputs as regression tests. Review and commit a failing input together with the
fix when it represents behavior the parser must preserve.

See the [Go fuzzing documentation](https://go.dev/doc/security/fuzz/) for the
engine's workflow and additional flags.
