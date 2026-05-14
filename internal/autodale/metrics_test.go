package autodale

import (
	"testing"
	"time"
)

func TestDailyEnergyReport(t *testing.T) {
	samples := []MetricsSample{
		{Timestamp: "2026-05-12T10:00:00Z", EstimatedWatts: 10},
		{Timestamp: "2026-05-12T11:00:00Z", EstimatedWatts: 20},
		{Timestamp: "2026-05-12T12:00:00Z", EstimatedWatts: 30},
	}
	report := DailyEnergyReport(samples, "2026-05-12", 0.40)
	if report.Samples != 3 {
		t.Fatalf("unexpected sample count: %#v", report)
	}
	if report.EnergyKWh != 0.05 {
		t.Fatalf("unexpected energy: %#v", report)
	}
	if report.EstimatedCost != 0.02 {
		t.Fatalf("unexpected cost: %#v", report)
	}
}

func TestDailyEnergyReportUsesLocalDay(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("CEST", 2*60*60)
	defer func() { time.Local = previousLocal }()

	samples := []MetricsSample{
		{Timestamp: "2026-05-12T22:30:00Z", EstimatedWatts: 10},
		{Timestamp: "2026-05-12T23:00:00Z", EstimatedWatts: 20},
	}
	report := DailyEnergyReport(samples, "2026-05-13", 0.40)
	if report.Samples != 2 {
		t.Fatalf("expected samples to count for local day: %#v", report)
	}
	if report.EnergyKWh != 0.01 {
		t.Fatalf("unexpected local-day energy: %#v", report)
	}
}
