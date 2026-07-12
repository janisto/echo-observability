package obs

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

const (
	defaultRequestIDHeader   = echo.HeaderXRequestID
	defaultTraceparentHeader = "traceparent"
	defaultTracestateHeader  = "tracestate"
	maxTracestateLen         = 512
)

var fallbackRequestIDCounter atomic.Uint64

type contextKey struct{}

type requestMetadata struct {
	RequestID     string
	CorrelationID string
	Trace         TraceContext
	Logger        *zap.Logger
}

// RequestContextConfig configures RequestContext middleware.
type RequestContextConfig struct {
	RequestIDHeader       string
	TraceparentHeader     string
	TracestateHeader      string
	ResponseHeader        string
	DisableResponseHeader bool
	NewRequestID          func() string
	ValidateRequestID     func(string) bool

	Logger *zap.Logger
	Preset Preset
}

// RequestContext returns Echo v5 middleware that installs request-scoped
// correlation metadata on the standard request context.
func RequestContext(config RequestContextConfig) echo.MiddlewareFunc {
	cfg := normalizeRequestContextConfig(config)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			metadata := metadataFromContext(c.Request().Context())
			if metadata == nil || metadata.RequestID == "" {
				existing := metadata
				metadata = buildRequestMetadata(c.Request().Header, cfg)
				if existing != nil && existing.Logger != nil {
					metadata.Logger = loggerWithMetadata(existing.Logger, metadata, cfg.Preset)
				}
				setRequestMetadata(c, metadata)
			}
			if cfg.Logger != nil && metadata.Logger == nil {
				setRequestLogger(c, metadata, loggerWithMetadata(cfg.Logger, metadata, cfg.Preset))
				metadata = metadataFromContext(c.Request().Context())
			}
			if !cfg.DisableResponseHeader {
				c.Response().Header().Set(cfg.ResponseHeader, metadata.RequestID)
			}
			return next(c)
		}
	}
}

// RequestID returns the validated or generated request ID for the context.
func RequestID(ctx context.Context) string {
	metadata := metadataFromContext(ctx)
	if metadata == nil {
		return ""
	}
	return metadata.RequestID
}

// CorrelationID returns the trace ID when a W3C trace exists, otherwise the
// request ID.
func CorrelationID(ctx context.Context) string {
	metadata := metadataFromContext(ctx)
	if metadata == nil {
		return ""
	}
	return metadata.CorrelationID
}

// Trace returns the parsed W3C trace context for the context, if one exists.
func Trace(ctx context.Context) TraceContext {
	metadata := metadataFromContext(ctx)
	if metadata == nil {
		return TraceContext{}
	}
	return metadata.Trace
}

func normalizeRequestContextConfig(config RequestContextConfig) RequestContextConfig {
	if config.RequestIDHeader == "" {
		config.RequestIDHeader = defaultRequestIDHeader
	}
	if config.TraceparentHeader == "" {
		config.TraceparentHeader = defaultTraceparentHeader
	}
	if config.TracestateHeader == "" {
		config.TracestateHeader = defaultTracestateHeader
	}
	if config.ResponseHeader == "" {
		config.ResponseHeader = config.RequestIDHeader
	}
	if config.NewRequestID == nil {
		config.NewRequestID = defaultNewRequestID
	}
	if config.ValidateRequestID == nil {
		config.ValidateRequestID = DefaultValidateRequestID
	}
	return config
}

func buildRequestMetadata(header http.Header, config RequestContextConfig) *requestMetadata {
	requestID := header.Get(config.RequestIDHeader)
	if !config.ValidateRequestID(requestID) {
		requestID = newValidRequestID(config.NewRequestID, config.ValidateRequestID)
	}

	trace, ok := ParseTraceparent(header.Get(config.TraceparentHeader))
	if ok {
		tracestate := strings.Join(header.Values(config.TracestateHeader), ",")
		if len(tracestate) <= maxTracestateLen {
			trace.Tracestate = tracestate
		}
	}
	correlationID := requestID
	if trace.Valid {
		correlationID = trace.TraceID
	}
	return &requestMetadata{RequestID: requestID, CorrelationID: correlationID, Trace: trace}
}

func ensureRequestMetadata(c *echo.Context, preset Preset) *requestMetadata {
	if metadata := metadataFromContext(c.Request().Context()); metadata != nil && metadata.RequestID != "" {
		return metadata
	}
	existing := metadataFromContext(c.Request().Context())
	config := normalizeRequestContextConfig(RequestContextConfig{})
	metadata := buildRequestMetadata(c.Request().Header, config)
	if existing != nil && existing.Logger != nil {
		metadata.Logger = loggerWithMetadata(existing.Logger, metadata, preset)
	}
	setRequestMetadata(c, metadata)
	if !config.DisableResponseHeader {
		c.Response().Header().Set(config.ResponseHeader, metadata.RequestID)
	}
	return metadata
}

func setRequestMetadata(c *echo.Context, metadata *requestMetadata) {
	ctx := contextWithRequestMetadata(c.Request().Context(), metadata)
	c.SetRequest(c.Request().WithContext(ctx))
}

func setRequestLogger(c *echo.Context, metadata *requestMetadata, logger *zap.Logger) {
	ctx := contextWithRequestLogger(c.Request().Context(), metadata, logger)
	c.SetRequest(c.Request().WithContext(ctx))
}

func contextWithRequestMetadata(ctx context.Context, metadata *requestMetadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, metadata)
}

func contextWithRequestLogger(ctx context.Context, metadata *requestMetadata, logger *zap.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = noopLogger
	}
	if metadata == nil {
		metadata = metadataFromContext(ctx)
	}
	if metadata == nil {
		metadata = &requestMetadata{}
	}
	next := *metadata
	next.Logger = logger
	return contextWithRequestMetadata(ctx, &next)
}

func metadataFromContext(ctx context.Context) *requestMetadata {
	if ctx == nil {
		return nil
	}
	metadata, ok := ctx.Value(contextKey{}).(*requestMetadata)
	if !ok {
		return nil
	}
	return metadata
}

func newValidRequestID(newRequestID func() string, validate func(string) bool) string {
	for range 2 {
		if id := newRequestID(); validate(id) {
			return id
		}
	}
	if id := fallbackRequestID(); validate(id) {
		return id
	}
	return "00000000000000000000000000000000"
}

func defaultNewRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fallbackRequestID()
	}
	return hex.EncodeToString(bytes[:])
}

func fallbackRequestID() string {
	var bytes [16]byte
	binary.BigEndian.PutUint64(bytes[8:], fallbackRequestIDCounter.Add(1))
	return hex.EncodeToString(bytes[:])
}

// DefaultValidateRequestID validates incoming request IDs accepted by the
// default middleware configuration.
func DefaultValidateRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-', c == '.', c == '_', c == '~':
		default:
			return false
		}
	}
	return true
}
