# Structured logging

`telegram-api` uses `github.com/sirupsen/logrus` with JSON output for structured logs.

## Logger initialization

```go
import applogger "github.com/Kotenkass/telegram-api/internal/logger"

log := applogger.NewLogger()
```

`NewLogger` reads `LOG_LEVEL` from the environment:

- `debug`
- `info` (default)
- `error`

Every log entry includes:

- `time`
- `level`
- `msg`
- `service_name=[service-name]`

## Error logging

```go
err := doWork()
if err != nil {
    log.WithError(err).
        WithField("user_id", userID).
        Error("do work failed")
}
```

## Business event logging

```go
log.WithFields(logrus.Fields{
    "chat_id":     chat.ID,
    "telegram_id": sender.ID,
    "username":    sender.Username,
}).Info("telegram user registered")
```

## Adding custom fields

```go
log.WithFields(logrus.Fields{
    "request_id": middleware.RequestID(c),
    "chat_id":    chat.ID,
    "duration_ms": elapsed.Milliseconds(),
}).Debug("business event")
```

## HTTP request middleware

Register the middleware when creating the Echo router:

```go
e := echo.New()
e.Use(middleware.RequestLogger(log))
```

Handlers can read the request ID from Echo context or the request context:

```go
requestID := middleware.RequestID(c)
requestIDFromContext := middleware.RequestIDFromContext(c.Request().Context())
```
