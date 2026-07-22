# E2E consumer

This Echo server uses a local module replacement to compile the exact package
checkout into a static, non-root distroless image. Echo's text banner and port
message are disabled so stdout remains object-only NDJSON.

```sh
just e2e-image observability-e2e-local:ci
```

Only the central observability repository evaluates cross-repository parity.
