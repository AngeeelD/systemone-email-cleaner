package policy

import (
	"strings"
	"testing"

	"emailcleaner/internal/config"
	"emailcleaner/internal/laya"
)

func defaultPolicy() config.Policy {
	return config.Policy{
		MinConfidenceJunk:  0.95,
		MinConfidenceTopic: 0.85,
	}
}

func defaultLabels() map[string]string {
	return map[string]string{
		"people":        "cleaner/people",
		"action":        "cleaner/action",
		"security":      "cleaner/security",
		"accounts":      "cleaner/accounts",
		"opportunities": "cleaner/opportunities",
		"banking":       "cleaner/banking",
		"unclassified":  "cleaner/unclassified",
	}
}

func ans(choice string, conf float64) laya.Answer {
	return laya.Answer{Choice: choice, Confidence: conf}
}

func equalLabels(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name       string
		answers    laya.Answers
		policy     config.Policy
		labels     map[string]string
		wantKind   Kind
		wantTrash  bool
		wantLabels []string
		wantReason string // substring that must appear in Reason; empty means no check
	}{
		{
			name: "junk above threshold with topics above does NOT trash (margin gate)",
			answers: laya.Answers{
				"is_junk":        ans("A", 0.95),
				"is_person":      ans("A", 0.99),
				"needs_action":   ans("A", 0.99),
				"is_security":    ans("A", 0.99),
				"is_purchase":    ans("A", 0.99),
				"is_opportunity": ans("A", 0.99),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people", "cleaner/action", "cleaner/security", "cleaner/accounts", "cleaner/opportunities"},
			wantReason: "is_person",
		},
		{
			name: "junk at threshold exactly 0.95 trash inclusive",
			answers: laya.Answers{
				"is_junk": ans("A", 0.95),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindTrash,
			wantTrash:  true,
			wantLabels: nil,
			wantReason: "is_junk",
		},
		{
			name: "junk just below threshold 0.94 not trash",
			answers: laya.Answers{
				"is_junk": ans("A", 0.94),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"cleaner/unclassified"},
			wantReason: "unclassified",
		},
		{
			name: "junk below threshold but topic above still labels",
			answers: laya.Answers{
				"is_junk":   ans("A", 0.94),
				"is_person": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "junk choice B even high conf not trash",
			answers: laya.Answers{
				"is_junk":   ans("B", 0.99),
				"is_person": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "junk lowercase a at threshold trash case-insensitive",
			answers: laya.Answers{
				"is_junk": ans("a", 0.95),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindTrash,
			wantTrash:  true,
			wantLabels: nil,
			wantReason: "is_junk",
		},
		{
			name: "junk with whitespace choice trimmed",
			answers: laya.Answers{
				"is_junk": ans(" A ", 0.95),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindTrash,
			wantTrash:  true,
			wantLabels: nil,
			wantReason: "is_junk",
		},
		{
			name: "junk missing treated as no topics may still apply",
			answers: laya.Answers{
				"is_person": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "is_person above threshold alone",
			answers: laya.Answers{
				"is_person": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "is_person below threshold unclassified",
			answers: laya.Answers{
				"is_person": ans("A", 0.84),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"cleaner/unclassified"},
			wantReason: "unclassified",
		},
		{
			name: "is_person at exactly 0.85 inclusive",
			answers: laya.Answers{
				"is_person": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "needs_action above threshold alone",
			answers: laya.Answers{
				"needs_action": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/action"},
			wantReason: "needs_action",
		},
		{
			name: "is_security above threshold alone",
			answers: laya.Answers{
				"is_security": ans("A", 0.90),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/security"},
			wantReason: "is_security",
		},
		{
			name: "is_purchase above maps to accounts label",
			answers: laya.Answers{
				"is_purchase": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/accounts"},
			wantReason: "is_purchase",
		},
		{
			name: "is_opportunity above threshold alone",
			answers: laya.Answers{
				"is_opportunity": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/opportunities"},
			wantReason: "is_opportunity",
		},
		{
			name: "is_security below threshold unclassified",
			answers: laya.Answers{
				"is_security": ans("A", 0.84),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"cleaner/unclassified"},
			wantReason: "unclassified",
		},
		{
			name: "multiple topics people+action together",
			answers: laya.Answers{
				"is_person":    ans("A", 0.85),
				"needs_action": ans("A", 0.91),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people", "cleaner/action"},
			wantReason: "is_person",
		},
		{
			name: "all five topics above",
			answers: laya.Answers{
				"is_person":      ans("A", 0.85),
				"needs_action":   ans("A", 0.85),
				"is_security":    ans("A", 0.85),
				"is_purchase":    ans("A", 0.85),
				"is_opportunity": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people", "cleaner/action", "cleaner/security", "cleaner/accounts", "cleaner/opportunities"},
			wantReason: "is_person",
		},
		{
			name: "all topics below threshold unclassified",
			answers: laya.Answers{
				"is_person":      ans("A", 0.84),
				"needs_action":   ans("A", 0.84),
				"is_security":    ans("A", 0.84),
				"is_purchase":    ans("A", 0.84),
				"is_opportunity": ans("A", 0.84),
				"is_junk":        ans("B", 0.90),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"cleaner/unclassified"},
			wantReason: "unclassified",
		},
		{
			name:       "nil answers map unclassified",
			answers:    nil,
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"cleaner/unclassified"},
			wantReason: "unclassified",
		},
		{
			name:       "empty answers map unclassified",
			answers:    laya.Answers{},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"cleaner/unclassified"},
			wantReason: "unclassified",
		},
		{
			name: "unknown choice value treated as no",
			answers: laya.Answers{
				"is_person": ans("C", 0.99),
				"is_junk":   ans("yes", 0.99),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"cleaner/unclassified"},
			wantReason: "unclassified",
		},
		{
			name: "topic choice B not applied even high conf",
			answers: laya.Answers{
				"is_person": ans("B", 0.99),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"cleaner/unclassified"},
			wantReason: "unclassified",
		},
		{
			name: "custom thresholds topic 0.80 filters",
			answers: laya.Answers{
				"is_person": ans("A", 0.75),
			},
			policy: config.Policy{
				MinConfidenceJunk:  0.90,
				MinConfidenceTopic: 0.80,
			},
			labels:     defaultLabels(),
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"cleaner/unclassified"},
			wantReason: "unclassified",
		},
		{
			name: "custom thresholds topic 0.80 passes when above",
			answers: laya.Answers{
				"is_person": ans("A", 0.85),
			},
			policy: config.Policy{
				MinConfidenceJunk:  0.90,
				MinConfidenceTopic: 0.80,
			},
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "custom junk threshold 0.95 not trash at 0.94",
			answers: laya.Answers{
				"is_junk":   ans("A", 0.94),
				"is_person": ans("A", 0.80),
			},
			policy: config.Policy{
				MinConfidenceJunk:  0.95,
				MinConfidenceTopic: 0.70,
			},
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "custom junk threshold 0.95 trash at exactly 0.95",
			answers: laya.Answers{
				"is_junk": ans("A", 0.95),
			},
			policy: config.Policy{
				MinConfidenceJunk:  0.95,
				MinConfidenceTopic: 0.70,
			},
			labels:     defaultLabels(),
			wantKind:   KindTrash,
			wantTrash:  true,
			wantLabels: nil,
			wantReason: "is_junk",
		},
		{
			name: "labels come from config not hardcoded custom people",
			answers: laya.Answers{
				"is_person": ans("A", 0.85),
			},
			policy: defaultPolicy(),
			labels: map[string]string{
				"people":        "my/people",
				"action":        "my/action",
				"security":      "my/security",
				"accounts":      "my/accounts",
				"opportunities": "my/opportunities",
				"unclassified":  "my/unclassified",
			},
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"my/people"},
			wantReason: "is_person",
		},
		{
			name: "unclassified custom label from config",
			answers: laya.Answers{
				"is_person": ans("A", 0.40),
			},
			policy: defaultPolicy(),
			labels: map[string]string{
				"people":       "my/people",
				"unclassified": "my/unclassified",
			},
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"my/unclassified"},
			wantReason: "unclassified",
		},
		{
			name: "nil labels map falls back to defaults",
			answers: laya.Answers{
				"is_person": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     nil,
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "missing label key falls back to default",
			answers: laya.Answers{
				"is_purchase": ans("A", 0.85),
			},
			policy: defaultPolicy(),
			labels: map[string]string{
				"people": "custom/people",
				// accounts key missing intentionally
			},
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/accounts"},
			wantReason: "is_purchase",
		},
		{
			name: "junk with whitespace and lowercase b not trash",
			answers: laya.Answers{
				"is_junk":   ans(" b ", 0.99),
				"is_person": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "reason contains confidence formatted for trash",
			answers: laya.Answers{
				"is_junk": ans("A", 0.97),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindTrash,
			wantTrash:  true,
			wantLabels: nil,
			wantReason: "0.97",
		},
		{
			name: "deterministic order people before action despite map iteration",
			answers: laya.Answers{
				"needs_action": ans("A", 0.85),
				"is_person":    ans("A", 0.85),
				"is_security":  ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people", "cleaner/action", "cleaner/security"},
			wantReason: "is_person",
		},
		{
			name: "is_banking above threshold alone",
			answers: laya.Answers{
				"is_banking": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/banking"},
			wantReason: "is_banking",
		},
		{
			name: "is_banking below threshold unclassified",
			answers: laya.Answers{
				"is_banking": ans("A", 0.84),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindUnclassified,
			wantTrash:  false,
			wantLabels: []string{"cleaner/unclassified"},
			wantReason: "unclassified",
		},
		{
			name: "is_banking at exactly 0.85 inclusive",
			answers: laya.Answers{
				"is_banking": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/banking"},
			wantReason: "is_banking",
		},
		{
			name: "junk 1.00 with is_banking 0.95 not trash but banking label",
			answers: laya.Answers{
				"is_junk":    ans("A", 1.00),
				"is_banking": ans("A", 0.95),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/banking"},
			wantReason: "is_banking",
		},
		{
			name: "junk 1.00 with is_purchase 0.90 not trash but accounts label",
			answers: laya.Answers{
				"is_junk":     ans("A", 1.00),
				"is_purchase": ans("A", 0.90),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/accounts"},
			wantReason: "is_purchase",
		},
		{
			name: "junk 1.00 with is_security 0.90 not trash",
			answers: laya.Answers{
				"is_junk":     ans("A", 1.00),
				"is_security": ans("A", 0.90),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/security"},
			wantReason: "is_security",
		},
		{
			name: "junk 1.00 with banking below threshold still trash",
			answers: laya.Answers{
				"is_junk":    ans("A", 1.00),
				"is_banking": ans("A", 0.84),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindTrash,
			wantTrash:  true,
			wantLabels: nil,
			wantReason: "is_junk",
		},
		{
			name: "all six topics above including banking",
			answers: laya.Answers{
				"is_person":      ans("A", 0.85),
				"needs_action":   ans("A", 0.85),
				"is_security":    ans("A", 0.85),
				"is_purchase":    ans("A", 0.85),
				"is_opportunity": ans("A", 0.85),
				"is_banking":     ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people", "cleaner/action", "cleaner/security", "cleaner/accounts", "cleaner/opportunities", "cleaner/banking"},
			wantReason: "is_person",
		},
		{
			name: "junk 1.00 with all six topics 0.90 not trash but six labels",
			answers: laya.Answers{
				"is_junk":        ans("A", 1.00),
				"is_person":      ans("A", 0.90),
				"needs_action":   ans("A", 0.90),
				"is_security":    ans("A", 0.90),
				"is_purchase":    ans("A", 0.90),
				"is_opportunity": ans("A", 0.90),
				"is_banking":     ans("A", 0.90),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people", "cleaner/action", "cleaner/security", "cleaner/accounts", "cleaner/opportunities", "cleaner/banking"},
			wantReason: "is_person",
		},
		{
			name: "banking custom label from config",
			answers: laya.Answers{
				"is_banking": ans("A", 0.85),
			},
			policy: defaultPolicy(),
			labels: map[string]string{
				"people":        "my/people",
				"action":        "my/action",
				"security":      "my/security",
				"accounts":      "my/accounts",
				"opportunities": "my/opportunities",
				"banking":       "my/banking",
				"unclassified":  "my/unclassified",
			},
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"my/banking"},
			wantReason: "is_banking",
		},
		{
			name: "banking deterministic order last",
			answers: laya.Answers{
				"is_banking":   ans("A", 0.85),
				"is_person":    ans("A", 0.85),
				"is_purchase":  ans("A", 0.85),
				"is_security":  ans("A", 0.85),
				"needs_action": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people", "cleaner/action", "cleaner/security", "cleaner/accounts", "cleaner/banking"},
			wantReason: "is_person",
		},
		{
			name: "is_banking choice case-insensitive and whitespace",
			answers: laya.Answers{
				"is_banking": ans(" a ", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/banking"},
			wantReason: "is_banking",
		},
		// Margin-gate specific cases (new thresholds 0.95/0.85 + 0.15 margin)
		{
			name: "margin: junk 1.00 vs banking 0.90 not trash (1.00 > 0.90+0.15 false)",
			answers: laya.Answers{
				"is_junk":    ans("A", 1.00),
				"is_banking": ans("A", 0.90),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/banking"},
			wantReason: "is_banking",
		},
		{
			name: "margin: junk 1.00 with max topic 0.84 below threshold still trash",
			answers: laya.Answers{
				"is_junk":    ans("A", 1.00),
				"is_banking": ans("A", 0.84),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindTrash,
			wantTrash:  true,
			wantLabels: nil,
			wantReason: "is_junk",
		},
		{
			name: "margin: junk 0.96 vs topic 0.85 not trash (0.96 > 1.00 false)",
			answers: laya.Answers{
				"is_junk":   ans("A", 0.96),
				"is_person": ans("A", 0.85),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "margin: junk with topic just at threshold not trash",
			answers: laya.Answers{
				"is_junk":     ans("A", 1.00),
				"is_person":   ans("A", 0.85),
				"is_purchase": ans("A", 0.86),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people", "cleaner/accounts"},
			wantReason: "is_person",
		},
		{
			name: "margin: custom low topic threshold allows junk to win when clearly above",
			answers: laya.Answers{
				"is_junk":   ans("A", 1.00),
				"is_person": ans("A", 0.70),
			},
			policy: config.Policy{
				MinConfidenceJunk:  0.95,
				MinConfidenceTopic: 0.70,
			},
			labels:     defaultLabels(),
			wantKind:   KindTrash,
			wantTrash:  true,
			wantLabels: nil,
			wantReason: "is_junk",
		},
		{
			name: "margin: custom low threshold but junk not enough above max",
			answers: laya.Answers{
				"is_junk":   ans("A", 0.95),
				"is_person": ans("A", 0.90),
			},
			policy: config.Policy{
				MinConfidenceJunk:  0.95,
				MinConfidenceTopic: 0.70,
			},
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people"},
			wantReason: "is_person",
		},
		{
			name: "margin: picks max confidence among topics",
			answers: laya.Answers{
				"is_junk":     ans("A", 1.00),
				"is_person":   ans("A", 0.86),
				"is_purchase": ans("A", 0.95),
				"is_banking":  ans("A", 0.88),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/people", "cleaner/accounts", "cleaner/banking"},
			wantReason: "is_person",
		},
		{
			name: "margin: ignores topics below threshold when computing max",
			answers: laya.Answers{
				"is_junk":    ans("A", 1.00),
				"is_person":  ans("A", 0.84),
				"is_banking": ans("A", 0.95),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindLabel,
			wantTrash:  false,
			wantLabels: []string{"cleaner/banking"},
			wantReason: "is_banking",
		},
		{
			name: "margin: ignores B choices when computing max",
			answers: laya.Answers{
				"is_junk":    ans("A", 0.99),
				"is_person":  ans("B", 0.99),
				"is_banking": ans("B", 0.99),
			},
			policy:     defaultPolicy(),
			labels:     defaultLabels(),
			wantKind:   KindTrash,
			wantTrash:  true,
			wantLabels: nil,
			wantReason: "is_junk",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.answers, tc.policy, tc.labels)
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.ShouldTrash != tc.wantTrash {
				t.Errorf("ShouldTrash = %v, want %v", got.ShouldTrash, tc.wantTrash)
			}
			if !equalLabels(got.Labels, tc.wantLabels) {
				t.Errorf("Labels = %v, want %v", got.Labels, tc.wantLabels)
			}
			if tc.wantReason != "" && !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("Reason = %q, want to contain %q", got.Reason, tc.wantReason)
			}
			// Invariant: trash has no labels; label/unclassified have at least one label
			if got.Kind == KindTrash && len(got.Labels) != 0 {
				t.Errorf("trash should have no labels, got %v", got.Labels)
			}
			if got.Kind == KindLabel && len(got.Labels) == 0 {
				t.Errorf("label kind should have at least one label")
			}
			if got.Kind == KindUnclassified && len(got.Labels) != 1 {
				t.Errorf("unclassified should have exactly one label, got %v", got.Labels)
			}
			// Reason must never be empty
			if got.Reason == "" {
				t.Errorf("Reason should not be empty")
			}
		})
	}
}

func TestDecide_PurityAndCopy(t *testing.T) {
	answers := laya.Answers{
		"is_person":    ans("A", 0.85),
		"needs_action": ans("A", 0.85),
	}
	labels := defaultLabels()
	got1 := Decide(answers, defaultPolicy(), labels)
	// Mutate returned slice should not affect next call
	if len(got1.Labels) > 0 {
		got1.Labels[0] = "mutated"
	}
	got2 := Decide(answers, defaultPolicy(), labels)
	if got2.Labels[0] == "mutated" {
		t.Error("Decide should return a copy of labels, not alias internal slice")
	}
	// Mutate input labels map should not affect previous result's defaults
	labels["people"] = "changed"
	got3 := Decide(answers, defaultPolicy(), map[string]string{"people": "cleaner/people", "action": "cleaner/action", "security": "cleaner/security", "accounts": "cleaner/accounts", "opportunities": "cleaner/opportunities", "banking": "cleaner/banking", "unclassified": "cleaner/unclassified"})
	if got3.Labels[0] != "cleaner/people" {
		t.Errorf("unexpected label after input mutation: %v", got3.Labels)
	}
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		action laya.Answers
		want   string
	}{
		// indirectly test Summarize via Decide
	}
	_ = tests
	a := Decide(laya.Answers{"is_junk": ans("A", 0.97)}, defaultPolicy(), defaultLabels())
	if s := Summarize(a); !strings.Contains(s, "TRASH") {
		t.Errorf("Summarize trash = %q, want TRASH", s)
	}
	b := Decide(laya.Answers{"is_person": ans("A", 0.85)}, defaultPolicy(), defaultLabels())
	if s := Summarize(b); !strings.Contains(s, "LABEL") {
		t.Errorf("Summarize label = %q, want LABEL", s)
	}
	c := Decide(laya.Answers{}, defaultPolicy(), defaultLabels())
	if s := Summarize(c); !strings.Contains(s, "UNCLASSIFIED") {
		t.Errorf("Summarize unclassified = %q, want UNCLASSIFIED", s)
	}
}
