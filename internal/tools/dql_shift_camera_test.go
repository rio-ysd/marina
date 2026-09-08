package tools

import (
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
