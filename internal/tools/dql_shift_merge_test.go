package tools

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04", s, cameraNotifyJST)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}

func TestMergeReportIntervalsAdjacent(t *testing.T) {
	rows := []dqlShiftViolation{
		{start: mustParse(t, "2026-08-10 09:00"), end: mustParse(t, "2026-08-10 10:30")},
		{start: mustParse(t, "2026-08-10 10:30"), end: mustParse(t, "2026-08-10 11:30")},
	}
	merged := mergeReportIntervals(rows)
	if len(merged) != 1 {
		t.Fatalf("len(merged) = %d, want 1", len(merged))
	}
	if !merged[0].start.Equal(mustParse(t, "2026-08-10 09:00")) || !merged[0].end.Equal(mustParse(t, "2026-08-10 11:30")) {
		t.Errorf("merged[0] = %v〜%v, want 09:00〜11:30", merged[0].start, merged[0].end)
	}
	if len(merged[0].overlaps) != 0 {
		t.Errorf("expected no overlaps for adjacent intervals, got %v", merged[0].overlaps)
	}
}

func TestMergeReportIntervalsOverlapping(t *testing.T) {
	rows := []dqlShiftViolation{
		{start: mustParse(t, "2026-08-10 09:00"), end: mustParse(t, "2026-08-10 10:30")},
		{start: mustParse(t, "2026-08-10 09:30"), end: mustParse(t, "2026-08-10 11:30")},
	}
	merged := mergeReportIntervals(rows)
	if len(merged) != 1 {
		t.Fatalf("len(merged) = %d, want 1", len(merged))
	}
	if !merged[0].start.Equal(mustParse(t, "2026-08-10 09:00")) || !merged[0].end.Equal(mustParse(t, "2026-08-10 11:30")) {
		t.Errorf("merged[0] = %v〜%v, want 09:00〜11:30", merged[0].start, merged[0].end)
	}
	if len(merged[0].overlaps) != 1 {
		t.Fatalf("len(overlaps) = %d, want 1", len(merged[0].overlaps))
	}
	ov := merged[0].overlaps[0]
	if !ov.start.Equal(mustParse(t, "2026-08-10 09:30")) || !ov.end.Equal(mustParse(t, "2026-08-10 10:30")) {
		t.Errorf("overlap = %v〜%v, want 09:30〜10:30", ov.start, ov.end)
	}
}

func TestMergeReportIntervalsGapStaysSeparate(t *testing.T) {
	rows := []dqlShiftViolation{
		{start: mustParse(t, "2026-08-10 09:00"), end: mustParse(t, "2026-08-10 10:00")},
		{start: mustParse(t, "2026-08-10 12:00"), end: mustParse(t, "2026-08-10 13:00")},
	}
	merged := mergeReportIntervals(rows)
	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}
}
