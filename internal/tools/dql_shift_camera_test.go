package tools

import (
	"database/sql"
	"testing"
	"time"
)

func TestFormatDurationJaMinutesOnly(t *testing.T) {
	if got := formatDurationJa(30 * time.Minute); got != "30分" {
		t.Errorf("formatDurationJa(30min) = %q", got)
	}
}

func TestFormatDurationJaExactHour(t *testing.T) {
	if got := formatDurationJa(60 * time.Minute); got != "1時間" {
		t.Errorf("formatDurationJa(60min) = %q", got)
	}
}

func TestFormatDurationJaHourAndMinutes(t *testing.T) {
	if got := formatDurationJa(90 * time.Minute); got != "1時間30分" {
		t.Errorf("formatDurationJa(90min) = %q", got)
	}
}

func TestResolveMonth(t *testing.T) {
	start, end, err := resolveMonth("2026-08")
	if err != nil {
		t.Fatalf("resolveMonth() error: %v", err)
	}
	if got := start.Format("2006-01-02"); got != "2026-08-01" {
		t.Errorf("start = %q", got)
	}
	if got := end.Format("2006-01-02"); got != "2026-08-31" {
		t.Errorf("end = %q", got)
	}
}

func TestResolveMonthRejectsDateRange(t *testing.T) {
	if _, _, err := resolveMonth("2026-08-01"); err == nil {
		t.Fatal("expected error for date range")
	}
}

func TestIsShortAttendanceDoesNotClassifyNoReportAsShortage(t *testing.T) {
	day := &monthlyAttendanceDay{
		shiftStart: sql.NullInt64{Int64: 540, Valid: true},
		shiftEnd:   sql.NullInt64{Int64: 1020, Valid: true},
	}
	if isShortAttendance(day) {
		t.Fatal("expected no report to be reported separately from shortage")
	}
}
