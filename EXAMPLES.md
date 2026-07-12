# Examples

This guide shows how to wire `echo-observability` into Echo v5 services and
how to keep the same log contract for generic JSON, Google Cloud, AWS, and
Azure deployments.

Runnable examples:

| Example | Purpose |
| --- | --- |
| [`examples/basic`](examples/basic) | Generic JSON for local or provider-neutral pipelines. |
| [`examples/gcp`](examples/gcp) | Google Cloud Logging field shape. |
| [`examples/aws`](examples/aws) | CloudWatch-friendly JSON and derived X-Ray trace ID. |
| [`examples/azure`](examples/azure) | Azure Monitor and Application Insights operation fields. |
| [`examples/local-wrapper/applog`](examples/local-wrapper/applog) | Optional application-local logging helpers. |

## Core Wiring

Every service follows the same shape:

1. Create a base logger with one preset.
2. Attach stable project fields.
3. Install `RequestContext` before `AccessLogger`.
4. Install recovery after `AccessLogger` when panics must be logged first.
5. Use `obs.Logger(ctx)` in handlers and services.

```go
logger, err := obs.NewLogger(obs.LoggerConfig{Preset: obs.PresetDefault})
if err != nil {
	return err
}
logger = logger.With(
	zap.String("service", envOrDefault("SERVICE_NAME", "echo-api")),
	zap.String("environment", envOrDefault("SERVICE_ENV", "local")),
	zap.String("version", envOrDefault("SERVICE_VERSION", "dev")),
)

e := echo.New()
e.Use(
	obs.RequestContext(obs.RequestContextConfig{Logger: logger}),
	obs.AccessLogger(obs.AccessLoggerConfig{Logger: logger}),
	middleware.Recover(),
)
```

For mixed Echo and standard-library routes, add one outer HTTP boundary:

```go
mux := http.NewServeMux()
mux.Handle("/", e)
mux.HandleFunc("GET /ready", readyHandler)

handler := obs.HTTPRequestContext(obs.HTTPRequestContextConfig{
	Logger: logger,
})(mux)
```

The inner Echo middleware reuses the outer metadata. `HTTPRequestContext`
does not write access logs, so Echo routes still produce exactly one access
line from `AccessLogger`.

## Echo Playground Migration

After `echo-playground` moves its application logging to Zap, replace its
local request ID, request logger, and access logger middleware with:

```go
e.Use(
	obs.RequestContext(obs.RequestContextConfig{
		Logger: logger,
		Preset: obs.PresetGCP,
	}),
	obs.AccessLogger(obs.AccessLoggerConfig{
		Logger: logger,
		Preset: obs.PresetGCP,
	}),
	appmiddleware.Security("/api-docs"),
	appmiddleware.Vary(),
	appmiddleware.CORS(),
	middleware.BodyLimit(1<<20),
	respond.Recoverer(),
)
```

Use `obs.RequestID(c.Request().Context())` instead of `c.Get("request_id")`.
Application logging becomes `obs.Logger(ctx).Info(...)`. Echo's internal
`e.Logger` remains its required `*slog.Logger`; it is not the application
request logger.

Set `e.IPExtractor` for the actual proxy topology. Access logs use
`c.RealIP()`, exactly like Echo's request logger.

Keep the observability pair before middleware whose rejected requests need
request IDs and access logs. Application errors mapped by a custom
`HTTPErrorHandler` must implement `echo.HTTPStatusCoder` so their logged status
matches the response status.

## Shared Environment Fields

Use stable field names across services:

| Variable | Log field | Example |
| --- | --- | --- |
| `SERVICE_NAME` | `service` | `echo-playground` |
| `SERVICE_ENV` | `environment` | `local`, `staging`, `prod` |
| `SERVICE_VERSION` | `version` | release tag, image tag, or commit SHA |
| `PORT` | none | `8080` |

Base fields appear on handler and access logs. `ExtraFields` is for
access-line-only request or response values:

```go
obs.AccessLogger(obs.AccessLoggerConfig{
	Logger: logger,
	ExtraFields: func(c *echo.Context) []zap.Field {
		return []zap.Field{zap.String("tenant_id", tenantID(c))}
	},
})
```

Do not log secrets, authorization headers, cookies, tokens, request bodies, or
unbounded user-controlled values.

## Run Locally

```sh
SERVICE_NAME=echo-example SERVICE_ENV=local SERVICE_VERSION=dev \
go run ./examples/basic
```

Call the Echo route:

```sh
curl -i -H 'X-Request-Id: demo-123' http://localhost:8080/health
```

Call the standard-library readiness route:

```sh
curl -i -H 'X-Request-Id: demo-456' http://localhost:8080/ready
```

Both responses use the same request-ID contract. The Echo route emits an
access line; the readiness handler emits only the explicit handler line.

## Handler And Service Logs

Handlers pass the request context into lower layers:

```go
func getHandler(c *echo.Context) error {
	ctx := c.Request().Context()
	obs.Logger(ctx).Info("hello get", zap.String("path", "/hello"))
	return loadGreeting(ctx)
}

func loadGreeting(ctx context.Context) error {
	obs.Logger(ctx).Debug("loading greeting")
	return nil
}
```

The installed logger already contains request and trace fields. Background
work that does not inherit a request context must receive an explicit process
logger instead of relying on `obs.Logger(context.Background())`.

## Request ID And Trace Headers

Send both correlation headers:

```sh
curl \
  -H 'X-Request-Id: demo-123' \
  -H 'traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01' \
  -H 'tracestate: vendor=value' \
  http://localhost:8080/health
```

The request ID remains `demo-123`, while `correlation_id` becomes the W3C
trace ID. Multiple `tracestate` header fields are combined in wire order.
Missing or invalid trace context falls back to the request ID.

Provider-specific legacy headers are not parsed. Use W3C Trace Context at the
application boundary.

## Route Templates And Names

Echo provides the matched route pattern through `c.Path()`. A request to
`/users/123` logs `path=/users/123` and `path_template=/users/:id`.

An explicit route name becomes `operation_id`:

```go
_, err := e.AddRoute(echo.Route{
	Method:  http.MethodGet,
	Path:    "/users/:id",
	Name:    "get-user",
	Handler: getUser,
})
```

Use `path_template` for aggregation to avoid high-cardinality route values.

## Google Cloud

```sh
GOOGLE_CLOUD_PROJECT=my-project GOOGLE_CLOUD_LOCATION=europe-north1 \
go run ./examples/gcp
```

Configure all components with `PresetGCP`. Access lines use `severity` and
contain `httpRequest`. Valid W3C context adds Google Cloud's preferred raw trace
ID and the sampled flag. The package intentionally does not manufacture a
current span ID from the incoming parent ID.

Representative access fields:

```json
{"severity":"INFO","message":"request completed","request_id":"demo-123","correlation_id":"4bf92f3577b34da6a3ce929d0e0e4736","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","logging.googleapis.com/trace":"4bf92f3577b34da6a3ce929d0e0e4736","logging.googleapis.com/trace_sampled":true,"method":"GET","path":"/health","path_template":"/health","status":200,"httpRequest":{"requestMethod":"GET","requestUrl":"http://localhost:8080/health","status":200}}
```

## AWS

```sh
AWS_REGION=eu-north-1 go run ./examples/aws
```

`PresetAWS` keeps flat JSON. A valid W3C trace ID is also formatted as
`xray_trace_id`, for example
`1-4bf92f35-77b34da6a3ce929d0e0e4736`. The package does not create X-Ray
segments or parse `X-Amzn-Trace-Id`.

## Azure

```sh
AZURE_REGION=northeurope AZURE_RESOURCE_GROUP=example-rg \
go run ./examples/azure
```

`PresetAzure` keeps flat JSON and maps valid W3C values to `operation_Id` and
`operation_ParentId`. The package does not initialize an Azure SDK or parse
legacy `Request-Id` headers.

## Error And Panic Behavior

Returned Echo errors are logged before being passed unchanged to the
centralized HTTP error handler. `echo.ResolveResponseStatus` determines the
status: a committed response wins, then an `HTTPStatusCoder`, then 500 for a
plain error.

On panic, `AccessLogger` emits a 500 line and re-panics. Recovery must be
inside it:

```go
e.Use(
	obs.RequestContext(obs.RequestContextConfig{Logger: logger}),
	obs.AccessLogger(obs.AccessLoggerConfig{Logger: logger}),
	respond.Recoverer(),
)
```

## Per-Project Checklist

- Use Echo v5 imports only.
- Use the same preset for logger and both middleware layers.
- Install request context before access logging.
- Put recovery after access logging.
- Pass `c.Request().Context()` to services.
- Group logs by `path_template`, not the concrete path.
- Keep provider tracing SDKs separate from this correlation package.
- Never place secrets or raw bodies in log fields.
- Run tests, race tests, vet, and lint before release.

## Optional Local Wrapper

[`examples/local-wrapper/applog`](examples/local-wrapper/applog) demonstrates
small `Debug`, `Info`, `Warn`, `Error`, and arbitrary-level helpers. The tests
prove that they use request metadata and do not mutate caller field slices.

## References

- [Echo middleware](https://echo.labstack.com/docs/category/middleware)
- [Echo error handling](https://echo.labstack.com/guide/error-handling/)
- [W3C Trace Context](https://www.w3.org/TR/trace-context/)
- [Google Cloud structured logging](https://cloud.google.com/logging/docs/structured-logging)
- [Google Cloud trace and log integration](https://docs.cloud.google.com/trace/docs/trace-log-integration)
- [Google Cloud Trace release notes](https://docs.cloud.google.com/trace/docs/release-notes)
- [AWS X-Ray trace header](https://docs.aws.amazon.com/xray/latest/devguide/xray-concepts.html#xray-concepts-tracingheader)
- [Azure Application Insights data model](https://learn.microsoft.com/azure/azure-monitor/app/data-model-complete)
