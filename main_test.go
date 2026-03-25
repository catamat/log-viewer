package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadLogFileSupportsGenericJSONValues(t *testing.T) {
	t.Helper()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "generic.jsonl")
	content := strings.Join([]string{
		`{"severity":"warn","msg":"slow db","ctx":{"attempt":2}}`,
		`["a","b"]`,
		`42`,
		`{"time":"2026-03-25T10:00:00Z","message":"hello"}`,
		`{invalid`,
	}, "\n")

	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp log: %v", err)
	}

	records, errs, nextOrder := readLogFile(filePath, 0)
	if len(records) != 4 {
		t.Fatalf("expected 4 parsed records, got %d", len(records))
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 parse error, got %d (%v)", len(errs), errs)
	}
	if nextOrder != 4 {
		t.Fatalf("expected next order to be 4, got %d", nextOrder)
	}

	first := records[0].Fields
	if first["severity"] != "warn" {
		t.Fatalf("expected severity field to be preserved, got %#v", first["severity"])
	}
	if first["msg"] != "slow db" {
		t.Fatalf("expected msg field to be preserved, got %#v", first["msg"])
	}
	if first["source file"] != "generic.jsonl" {
		t.Fatalf("expected source file metadata, got %#v", first["source file"])
	}
	if first["line number"] != 1 {
		t.Fatalf("expected line number 1, got %#v", first["line number"])
	}

	second := records[1].Fields
	if _, ok := second["value"].([]any); !ok {
		t.Fatalf("expected array line to be wrapped into value column, got %#v", second["value"])
	}

	third := records[2].Fields
	if third["value"] != float64(42) {
		t.Fatalf("expected numeric line to be wrapped into value column, got %#v", third["value"])
	}

	last := records[3]
	expectedTime := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	if !last.HasSortTime {
		t.Fatal("expected time field to be detected as sortable")
	}
	if !last.SortTime.Equal(expectedTime) {
		t.Fatalf("expected sort time %v, got %v", expectedTime, last.SortTime)
	}
}

func TestCollectColumnsKeepsUsefulOrder(t *testing.T) {
	records := []parsedLogRecord{
		{
			Fields: map[string]any{
				"message":     "hello",
				"ctx":         map[string]any{"attempt": 1},
				"source file": "app.log",
				"line number": 1,
			},
		},
		{
			Fields: map[string]any{
				"severity":    "warn",
				"timestamp":   "2026-03-25T10:00:00Z",
				"source file": "app.log",
				"line number": 2,
			},
		},
	}

	columns := collectColumns(records)
	want := []string{"timestamp", "severity", "message", "ctx", "source file", "line number"}

	if len(columns) != len(want) {
		t.Fatalf("expected %d columns, got %d (%v)", len(want), len(columns), columns)
	}

	for i := range want {
		if columns[i] != want[i] {
			t.Fatalf("expected column %d to be %q, got %q (%v)", i, want[i], columns[i], columns)
		}
	}
}
