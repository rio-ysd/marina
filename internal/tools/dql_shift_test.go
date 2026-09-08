package tools

import (
	"database/sql"
	"testing"
	"time"
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
	if got := withLastName.label(); got != "藤原香織(dql id=4)" {
		t.Errorf("label() = %q", got)
	}
	withoutLastName := dqlStaff{id: 7, name: "MAHO"}
	if got := withoutLastName.label(); got != "MAHO(dql id=7)" {
		t.Errorf("label() = %q", got)
	}
}

func TestIsOutsideShiftWithinRange(t *testing.T) {
	v := dqlShiftViolation{
		start:    time.Date(2026, 9, 4, 9, 30, 0, 0, cameraNotifyJST),
		end:      time.Date(2026, 9, 4, 16, 0, 0, 0, cameraNotifyJST),
		startMin: sql.NullInt64{Int64: 540, Valid: true},
		endMin:   sql.NullInt64{Int64: 1020, Valid: true},
	}
	if isOutsideShift(v) {
		t.Error("expected within shift, got outside")
	}
}

func TestIsOutsideShiftStartsEarly(t *testing.T) {
	v := dqlShiftViolation{
		start:    time.Date(2026, 9, 4, 8, 0, 0, 0, cameraNotifyJST),
		end:      time.Date(2026, 9, 4, 12, 0, 0, 0, cameraNotifyJST),
		startMin: sql.NullInt64{Int64: 540, Valid: true},
		endMin:   sql.NullInt64{Int64: 1020, Valid: true},
	}
	if !isOutsideShift(v) {
		t.Error("expected outside shift, got within")
	}
}

func TestIsOutsideShiftEndsLate(t *testing.T) {
	v := dqlShiftViolation{
		start:    time.Date(2026, 9, 4, 10, 0, 0, 0, cameraNotifyJST),
		end:      time.Date(2026, 9, 4, 18, 0, 0, 0, cameraNotifyJST),
		startMin: sql.NullInt64{Int64: 540, Valid: true},
		endMin:   sql.NullInt64{Int64: 1020, Valid: true},
	}
	if !isOutsideShift(v) {
		t.Error("expected outside shift, got within")
	}
}

func TestIsOutsideShiftNoShift(t *testing.T) {
	v := dqlShiftViolation{
		start: time.Date(2026, 9, 4, 10, 0, 0, 0, cameraNotifyJST),
		end:   time.Date(2026, 9, 4, 12, 0, 0, 0, cameraNotifyJST),
	}
	if !isOutsideShift(v) {
		t.Error("expected outside shift (no shift registered), got within")
	}
}
