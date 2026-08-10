package logger

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestNewLoggerWritesStructuredJSON(t *testing.T) {
	t.Setenv(EnvLogLevel, "debug")

	var output bytes.Buffer
	l := NewLogger()
	l.SetOutput(&output)
	l.WithField("business_event", "user_registered").Debug("debug message")

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry); err != nil {
		t.Fatalf("decode log entry: %v", err)
	}

	if entry["time"] == nil {
		t.Fatal("expected time field")
	}
	if entry["level"] != "debug" {
		t.Fatalf("expected debug level, got %v", entry["level"])
	}
	if entry["service_name"] != ServiceName {
		t.Fatalf("expected service_name %q, got %v", ServiceName, entry["service_name"])
	}
	if entry["business_event"] != "user_registered" {
		t.Fatalf("expected custom field, got %v", entry["business_event"])
	}
	if entry["msg"] != "debug message" {
		t.Fatalf("expected message, got %v", entry["msg"])
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  logrus.Level
	}{
		{name: "debug", value: "debug", want: logrus.DebugLevel},
		{name: "info", value: "info", want: logrus.InfoLevel},
		{name: "error", value: "error", want: logrus.ErrorLevel},
		{name: "empty defaults to info", value: "", want: logrus.InfoLevel},
		{name: "invalid defaults to info", value: "trace", want: logrus.InfoLevel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseLevel(tt.value); got != tt.want {
				t.Fatalf("parseLevel(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
