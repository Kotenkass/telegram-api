package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

const (
	RequestIDHeader = "X-Request-ID"
	RequestIDKey    = "request_id"
)

type requestIDContextKey struct{}

// RequestLogger logs each incoming HTTP request with structured fields and execution time.
func RequestLogger(logger *logrus.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			requestID := c.Request().Header.Get(RequestIDHeader)
			if requestID == "" {
				requestID = uuid.NewString()
			}

			c.Request().Header.Set(RequestIDHeader, requestID)
			c.Response().Header().Set(RequestIDHeader, requestID)
			c.Set(RequestIDKey, requestID)
			c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), requestIDContextKey{}, requestID)))

			err := next(c)
			elapsed := time.Since(start)

			status := c.Response().Status
			if status == 0 {
				if err != nil {
					status = http.StatusInternalServerError
				} else {
					status = http.StatusOK
				}
			}

			logger.WithFields(logrus.Fields{
				"request_id":   requestID,
				"method":       c.Request().Method,
				"path":         c.Request().URL.Path,
				"status":       status,
				"execution_ms": elapsed.Milliseconds(),
			}).Info("http request")

			return err
		}
	}
}

// RequestID returns the request ID from Echo context, if present.
func RequestID(c echo.Context) string {
	if value, ok := c.Get(RequestIDKey).(string); ok {
		return value
	}
	return ""
}

// RequestIDFromContext returns the request ID from a Go context, if present.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(requestIDContextKey{}).(string); ok {
		return value
	}
	return ""
}
