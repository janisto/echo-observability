# Examples

This guide shows how to wire `echo-observability` into Echo v5 services while
keeping one log contract across Google Cloud, provider-neutral, AWS, and Azure
deployments.

When one configuration is shown, this project uses GCP as the canonical
example. The other runnable applications remain first-class and tested.

| Example | Purpose |
| --- | --- |
| [`examples/gcp`](examples/gcp) | Canonical Google Cloud Logging field shape. |
| [`examples/basic`](examples/basic) | Generic JSON for local or provider-neutral pipelines. |
| [`examples/aws`](examples/aws) | CloudWatch-friendly JSON and a derived X-Ray trace ID. |
| [`examples/azure`](examples/azure) | Azure Monitor and Application Insights operation fields. |
| [`examples/local-wrapper/applog`](examples/local-wrapper/applog) | Optional application-local logging helpers. |

## Core Wiring

Every service follows the same shape:

1. Create one logger with the selected preset.
2. Install `RequestContext` before `AccessLogger` with the same logger and preset.
3. Install recovery before `AccessLogger` so access logging observes and
   rethrows panics before recovery turns them into application responses.
4. Use `obs.Logger(ctx)` in handlers and services.
5. Add service-specific fields directly to application logs; they do not leak
   into the separate access record.

The canonical GCP wiring is:

```go
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
	obs.RequestContext(obs.RequestContextConfig{
		Logger: logger,
		Preset: obs.PresetGCP,
	}),
	middleware.Recover(),
	obs.AccessLogger(obs.AccessLoggerConfig{
		Logger:            logger,
		Preset:            obs.PresetGCP,
		GCPProfileVersion: profileVersion,
	}),
)
```

No Google Cloud project ID is required. With valid W3C context,
`logging.googleapis.com/trace` contains the raw trace ID.

## Run The Canonical GCP Example

```bash
go run ./examples/gcp
```

Call the health route with request and trace correlation:

```bash
curl -i \
  -H 'X-Request-ID: demo-123' \
  -H 'traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01' \
  -H 'tracestate: vendor=value' \
  http://127.0.0.1:8080/health
```

The request ID remains `demo-123`; `correlation_id` becomes the W3C trace ID.
The info-level health record, debug-level dependency record, and terminal
access record contain the same correlation fields. Application records retain
developer-defined service fields. The access record remains separate and
contains `httpRequest`, `/health` as the route template, `health_check` as the
operation ID, and status 200.

The runnable GCP example opts into debug output. `NewLogger` defaults to info,
which suppresses the dependency record and its fields while preserving the
health and access records.

Representative GCP fields:

```json
{"severity":"INFO","message":"health check","request_id":"demo-123","correlation_id":"4bf92f3577b34da6a3ce929d0e0e4736","service_name":"example-service","service_version":"1.0.0","health_status":"ok"}
{"severity":"DEBUG","message":"dependency check","request_id":"demo-123","correlation_id":"4bf92f3577b34da6a3ce929d0e0e4736","dependency":"database","dependency_status":"ok","check_duration_ms":3}
{"severity":"INFO","message":"request completed","request_id":"demo-123","correlation_id":"4bf92f3577b34da6a3ce929d0e0e4736","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","logging.googleapis.com/trace":"4bf92f3577b34da6a3ce929d0e0e4736","logging.googleapis.com/trace_sampled":true,"method":"GET","duration_ms":12.5,"path_template":"/health","operation_id":"health_check","status":200,"httpRequest":{"requestMethod":"GET","status":200,"latency":"0.0125s"}}
```

The package does not create spans and therefore does not manufacture
`logging.googleapis.com/spanId` from the incoming parent ID.

W3C Trace Context Level 1 is the default. To enable the pinned Level 2 mode,
configure `TraceContextLevel2` on both `RequestContext` and `AccessLogger`.
Level 2 adds `trace_id_random`, derived from bit one of the preserved
two-character `trace_flags`. Unsupported levels fail at middleware
construction. Duplicate request-ID or `traceparent` field-lines are rejected
as ambiguous, and `tracestate` is retained only after complete selected-level
grammar, duplicate-key, and 32-member validation. A valid value is not rejected
merely because it exceeds the 512-character minimum propagation capacity.

Raw path, direct peer IP, and user agent are disabled by default and have
independent access-log opt-ins. GCP does not change those defaults. Captured
GCP `requestUrl` is path-only. The unpinned installed GCP profile resolves to
`0.1.0`; use `GCPProfileVersionV0_1_0` for an exact pin.

## Provider-Neutral JSON

```bash
go run ./examples/basic
```

The default preset writes `level` and the generic correlation fields without
provider-specific trace aliases.

## AWS

```bash
go run ./examples/aws
```

The AWS preset keeps flat JSON. A valid W3C trace ID is also formatted as
`xray_trace_id`, for example
`1-4bf92f35-77b34da6a3ce929d0e0e4736`. The package does not create X-Ray
segments or parse `X-Amzn-Trace-Id`. The exact current profile is `0.1.0`;
omission resolves to it, and `AWSProfileVersionV0_1_0` pins it.

## Azure

```bash
go run ./examples/azure
```

The Azure preset maps valid W3C values to `operation_Id` and
`operation_ParentId`. It does not initialize an Azure SDK or parse legacy
`Request-Id` headers. The exact current profile is `0.1.0`; omission resolves
to it, and `AzureProfileVersionV0_1_0` pins it.

## Mixed Echo And `net/http` Routes

Install the same GCP configuration at the outer router boundary when one
service has both Echo and non-Echo routes:

```go
mux := http.NewServeMux()
mux.Handle("/", e)
mux.HandleFunc("GET /ready", readyHandler)

handler := obs.HTTPRequestContext(obs.HTTPRequestContextConfig{
	Logger: logger,
	Preset: obs.PresetGCP,
})(mux)
```

The inner Echo middleware reuses the outer request metadata. Non-Echo access
logging remains application-owned.

## Optional Local Wrapper

[`examples/local-wrapper/applog`](examples/local-wrapper/applog) provides small
`Debug`, `Info`, `Warn`, `Error`, and arbitrary-level helpers around
`obs.Logger(ctx)`. It is a convenience layer, not required package
configuration. Passing `context.Context` keeps request and trace correlation
without coupling helpers to Echo.

```go
applog.Info(ctx, "loading item", zap.String("item_id", itemID))
applog.Error(ctx, "item load failed", err, zap.String("item_id", itemID))
```

Tests verify that the wrapper preserves request metadata, structured fields,
levels, and error information.

## Per-Project Checklist

- Use Go 1.25 or newer and Echo v5.
- Use GCP when documentation needs one representative configuration.
- Keep runnable examples limited to required package wiring.
- Use the same preset for the logger and all observability middleware.
- Install `RequestContext` before `AccessLogger`.
- Group logs by `path_template`, not the concrete request path.
- Keep provider tracing SDKs separate from this correlation package.
- Never place secrets or raw bodies in log fields.
- Run formatting, lint, tests, and race tests.

## References

- [Google Cloud: Link log entries with traces](https://docs.cloud.google.com/trace/docs/trace-log-integration)
- [Google Cloud Trace release notes](https://docs.cloud.google.com/trace/docs/release-notes)
- [Google Cloud structured logging](https://cloud.google.com/logging/docs/structured-logging)
- [W3C Trace Context](https://www.w3.org/TR/trace-context/)
- [W3C Trace Context Level 2](https://www.w3.org/TR/2024/CRD-trace-context-2-20240328/)
