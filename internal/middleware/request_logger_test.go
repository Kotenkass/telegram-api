package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

func TestRequestLoggerAddsRequestIDToContextAndResponse(t *testing.T) {
	var output bytes.Buffer
	log := logrus.New()
	log.SetOutput(&output)
	log.SetFormatter(&logrus.JSONFormatter{})

	e := echo.New()
	e.Use(RequestLogger(log))

	var contextID string
	e.GET("/users", func(c echo.Context) error {
		contextID = RequestID(c)
		if RequestIDFromContext(c.Request().Context()) != contextID {
			t.Fatalf("expected request ID in request context, got %q", RequestIDFromContext(c.Request().Context()))
		}
		return c.String(http.StatusCreated, "created")
	})

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	req.Header.Set(RequestIDHeader, "incoming-request-id")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}
	if rec.Header().Get(RequestIDHeader) != "incoming-request-id" {
		t.Fatalf("expected response header %q, got %q", "incoming-request-id", rec.Header().Get(RequestIDHeader))
	}
	if contextID != "incoming-request-id" {
		t.Fatalf("expected context request ID %q, got %q", "incoming-request-id", contextID)
	}

	entry := decodeLogEntry(t, output.Bytes())
	if entry["request_id"] != "incoming-request-id" {
		t.Fatalf("expected request_id %q, got %v", "incoming-request-id", entry["request_id"])
	}
	if entry["method"] != http.MethodGet {
		t.Fatalf("expected method %q, got %v", http.MethodGet, entry["method"])
	}
	if entry["path"] != "/users" {
		t.Fatalf("expected path %q, got %v", "/users", entry["path"])
	}
	if entry["status"] != float64(http.StatusCreated) {
		t.Fatalf("expected status %d, got %v", http.StatusCreated, entry["status"])
	}
	if _, ok := entry["execution_ms"]; !ok {
		t.Fatal("expected execution_ms field")
	}
}

func TestRequestLoggerGeneratesRequestID(t *testing.T) {
	var output bytes.Buffer
	log := logrus.New()
	log.SetOutput(&output)
	log.SetFormatter(&logrus.JSONFormatter{})

	e := echo.New()
	e.Use(RequestLogger(log))

	var contextID string
	e.GET("/healthz", func(c echo.Context) error {
		contextID = RequestID(c)
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	generatedID := rec.Header().Get(RequestIDHeader)
	if generatedID == "" {
		t.Fatal("expected generated request ID")
	}
	if contextID != generatedID {
		t.Fatalf("expected context request ID %q, got %q", generatedID, contextID)
	}

	entry := decodeLogEntry(t, output.Bytes())
	if entry["request_id"] != generatedID {
		t.Fatalf("expected request_id %q, got %v", generatedID, entry["request_id"])
	}
}

func decodeLogEntry(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &entry); err != nil {
		t.Fatalf("decode log entry: %v", err)
	}
	return entry
}
