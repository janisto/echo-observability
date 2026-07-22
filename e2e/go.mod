module github.com/janisto/echo-observability-e2e

go 1.26.0

require (
	github.com/janisto/echo-observability/v2 v2.0.0
	github.com/labstack/echo/v5 v5.3.0
	go.uber.org/zap v1.28.0
)

require (
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/janisto/echo-observability/v2 => ..
