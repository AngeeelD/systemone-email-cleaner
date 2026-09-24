package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/AngeeelD/systemone-email-cleaner/internal/config"
	"github.com/AngeeelD/systemone-email-cleaner/internal/systemone"
)

func tuneTestConfig() *config.Config {
	return &config.Config{
		Policy: config.Policy{MinConfidenceJunk: 0.60, MinConfidenceTopic: 0.60},
		Labels: map[string]string{
			"people":       "cleaner/people",
			"banking":      "cleaner/banking",
			"accounts":     "cleaner/accounts",
			"security":     "cleaner/security",
			"unclassified": "cleaner/unclassified",
		},
	}
}

func ansA(p float64) systemone.Answer {
	return systemone.Answer{Choice: "A", Probabilities: map[string]float64{"A": p, "B": 1 - p}}
}

func TestSweepCountsBuckets(t *testing.T) {
	samples := []systemone.Answers{
		{"is_junk": ansA(0.90)},                             // trash
		{"is_banking": ansA(0.65)},                          // one label
		{"is_person": ansA(0.80), "is_banking": ansA(0.70)}, // two labels = spillover
		{}, // unclassified
	}
	p := config.Policy{MinConfidenceJunk: 0.60, MinConfidenceTopic: 0.60}

	trash, label, unclass, spill := sweepCounts(samples, p, tuneTestConfig().Labels)
	if trash != 1 || label != 2 || unclass != 1 || spill != 1 {
		t.Errorf("counts = trash %d label %d unclass %d spill %d, want 1/2/1/1", trash, label, unclass, spill)
	}
}

// A lower topic threshold must move a message from unclassified into a label.
func TestSweepCountsTopicThresholdMovesUnclassified(t *testing.T) {
	samples := []systemone.Answers{{"is_banking": ansA(0.57)}}
	labels := tuneTestConfig().Labels

	_, labelLow, _, _ := sweepCounts(samples, config.Policy{MinConfidenceJunk: 0.60, MinConfidenceTopic: 0.55}, labels)
	_, labelHigh, unclassHigh, _ := sweepCounts(samples, config.Policy{MinConfidenceJunk: 0.60, MinConfidenceTopic: 0.60}, labels)

	if labelLow != 1 {
		t.Errorf("at topic 0.55 labels = %d, want 1 (0.57 clears it)", labelLow)
	}
	if labelHigh != 0 || unclassHigh != 1 {
		t.Errorf("at topic 0.60 labels = %d unclass = %d, want 0/1", labelHigh, unclassHigh)
	}
}

func TestPrintSweepMarksTheCurrentConfig(t *testing.T) {
	var out bytes.Buffer
	printSweep(&out, tuneTestConfig(), []systemone.Answers{{"is_junk": ansA(0.90)}})

	var marked string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "*") {
			marked = line
		}
	}
	if marked == "" {
		t.Fatalf("no line marks the current config:\n%s", out.String())
	}
	if strings.Count(marked, "0.60") != 2 {
		t.Errorf("marked line = %q, want the current 0.60/0.60 config", marked)
	}
	if !strings.Contains(out.String(), "junk  topic") {
		t.Errorf("sweep is missing its header:\n%s", out.String())
	}
}
