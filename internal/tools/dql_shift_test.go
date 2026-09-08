package tools

import (
	"database/sql"
	"testing"
)

func TestFormatMinuteRange(t *testing.T) {
	if got := formatMinuteRange(540, 1020); got != "09:00-17:00" {
		t.Errorf("formatMinuteRange(540, 1020) = %q", got)
	}
}

func TestResolveDQLDateRangeDefaults(t *testing.T) {
	from, to, err := resolveDQLDateRange("", "")
	if err != nil {
		t.Fatalf("resolveDQLDateRange() error: %v", err)
	}
	if from == "" || to == "" {
		t.Fatalf("expected non-empty from/to, got from=%q to=%q", from, to)
	}
}

func TestResolveDQLDateRangeExplicit(t *testing.T) {
	from, to, err := resolveDQLDateRange("2026-08-08", "2026-09-08")
	if err != nil {
		t.Fatalf("resolveDQLDateRange() error: %v", err)
	}
	if from != "2026-08-08" || to != "2026-09-08" {
		t.Errorf("got from=%q to=%q", from, to)
	}
}

func TestResolveDQLDateRangeInvalid(t *testing.T) {
	if _, _, err := resolveDQLDateRange("invalid", ""); err == nil {
		t.Error("expected error for invalid from, got nil")
	}
}

func TestDqlStaffLabel(t *testing.T) {
	withLastName := dqlStaff{id: 4, name: "香織", lastName: sql.NullString{String: "藤原", Valid: true}}
	if got := withLastName.label(); got != "藤原香織(id=4)" {
		t.Errorf("label() = %q", got)
	}
	withoutLastName := dqlStaff{id: 7, name: "MAHO"}
	if got := withoutLastName.label(); got != "MAHO(id=7)" {
		t.Errorf("label() = %q", got)
	}
}
