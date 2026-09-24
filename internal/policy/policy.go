// Package policy is a pure deterministic classifier that maps Laya answers
// to a Gmail action for dry-run. It performs no I/O and no network calls.
package policy

import (
	"fmt"
	"strings"

	"emailcleaner/internal/config"
	"emailcleaner/internal/laya"
)

// Kind is the decision outcome.
type Kind string

const (
	KindTrash        Kind = "trash"
	KindLabel        Kind = "label"
	KindUnclassified Kind = "unclassified"
)

// Action is the result of Decide. It is pure data: callers decide how to
// apply it (dry-run print, Gmail batchModify, audit log, etc.).
//
// Labels holds Gmail label names to add. For KindTrash it is empty. For
// KindUnclassified it holds a single entry (cleaner/unclassified or the
// configured equivalent). INBOX is never removed except when ShouldTrash is
// true; callers must respect that invariant.
type Action struct {
	Kind        Kind     `json:"kind"`
	Labels      []string `json:"labels,omitempty"`
	Reason      string   `json:"reason"`
	ShouldTrash bool     `json:"should_trash"`
}

// topicDef binds a Laya question name to a label config key and its default.
type topicDef struct {
	Question     string
	LabelKey     string
	DefaultLabel string
}

// topicOrder is the deterministic order in which topics are evaluated and
// reported. Changing the order would change the audit reason string and the
// label slice order, so it is intentionally fixed and matches the spec table.
var topicOrder = []topicDef{
	{Question: "is_person", LabelKey: "people", DefaultLabel: "cleaner/people"},
	{Question: "needs_action", LabelKey: "action", DefaultLabel: "cleaner/action"},
	{Question: "is_security", LabelKey: "security", DefaultLabel: "cleaner/security"},
	{Question: "is_purchase", LabelKey: "accounts", DefaultLabel: "cleaner/accounts"},
	{Question: "is_opportunity", LabelKey: "opportunities", DefaultLabel: "cleaner/opportunities"},
	{Question: "is_banking", LabelKey: "banking", DefaultLabel: "cleaner/banking"},
}

// Decide applies the four-step policy from the spec in order:
//
//  1. is_junk == A and confidence >= min_confidence_junk → Trash ONLY if
//     is_junk confidence exceeds the highest topic confidence (among topics
//     where choice is A and conf >= min_confidence_topic) by more than 0.15.
//     If no topic clears its threshold, junk wins immediately. This margin
//     gate counters overconfidence (e.g. is_junk 1.00 alongside banking 0.90)
//     where the previous "no topic >= threshold" gate was too permissive.
//  2. For each topic answered A with confidence >= min_confidence_topic → add its label.
//     Includes is_banking → cleaner/banking.
//  3. Not junk and no topic cleared threshold → cleaner/unclassified.
//  4. INBOX is never removed except when Trash (ShouldTrash).
//
// It is pure: no I/O, no mutation of inputs, deterministic output. Missing
// answers are treated as "no". Choice comparison is case-insensitive and
// whitespace-trimmed. Threshold comparison is inclusive (>=).
func Decide(answers laya.Answers, cfg config.Policy, labels map[string]string) Action {
	// Step 1: junk gate with margin — trash only if junk clearly beats best topic.
	if ans, ok := answers["is_junk"]; ok && isYes(ans, cfg.MinConfidenceJunk) {
		maxTopicConf, hasTopic := maxTopicConfidenceFiltered(answers, cfg.MinConfidenceTopic)
		if !hasTopic {
			return Action{
				Kind:        KindTrash,
				Labels:      nil,
				ShouldTrash: true,
				Reason:      fmt.Sprintf("is_junk conf=%.2f >= %.2f", ans.Confidence, cfg.MinConfidenceJunk),
			}
		}
		if ans.Confidence > maxTopicConf+0.15 {
			return Action{
				Kind:        KindTrash,
				Labels:      nil,
				ShouldTrash: true,
				Reason:      fmt.Sprintf("is_junk conf=%.2f > max_topic conf=%.2f + 0.15", ans.Confidence, maxTopicConf),
			}
		}
	}

	// Step 2: topic labels (multi-label).
	var toAdd []string
	var reasonParts []string
	for _, td := range topicOrder {
		ans, ok := answers[td.Question]
		if !ok {
			continue
		}
		if !isYes(ans, cfg.MinConfidenceTopic) {
			continue
		}
		label := td.DefaultLabel
		if v, ok := labels[td.LabelKey]; ok && strings.TrimSpace(v) != "" {
			label = v
		}
		toAdd = append(toAdd, label)
		reasonParts = append(reasonParts, fmt.Sprintf("%s conf=%.2f", td.Question, ans.Confidence))
	}

	if len(toAdd) > 0 {
		// Return a copy to avoid aliasing internal slice on future appends.
		out := make([]string, len(toAdd))
		copy(out, toAdd)
		return Action{
			Kind:        KindLabel,
			Labels:      out,
			ShouldTrash: false,
			Reason:      strings.Join(reasonParts, ", "),
		}
	}

	// Step 3: unclassified fallback.
	unclassifiedLabel := "cleaner/unclassified"
	if v, ok := labels["unclassified"]; ok && strings.TrimSpace(v) != "" {
		unclassifiedLabel = v
	}

	// Build a helpful reason that includes the highest topic confidence when available.
	reason := fmt.Sprintf("unclassified: no topic >= %.2f", cfg.MinConfidenceTopic)
	if maxQ, maxConf, ok := maxTopicConfidence(answers); ok {
		reason = fmt.Sprintf("unclassified: no topic >= %.2f (max %s conf=%.2f)", cfg.MinConfidenceTopic, maxQ, maxConf)
	}

	return Action{
		Kind:        KindUnclassified,
		Labels:      []string{unclassifiedLabel},
		ShouldTrash: false,
		Reason:      reason,
	}
}

// isYes reports whether ans is an affirmative (choice A) with confidence at
// or above threshold. Choice is trimmed and case-insensitive; any other value
// (B, C, "", "yes") is treated as negative.
func isYes(ans laya.Answer, threshold float64) bool {
	choice := strings.TrimSpace(ans.Choice)
	if !strings.EqualFold(choice, "A") {
		return false
	}
	return ans.Confidence >= threshold
}

// maxTopicConfidence returns the topic question with the highest confidence
// among present answers, regardless of choice value. Used only for the
// unclassified reason string.
func maxTopicConfidence(answers laya.Answers) (string, float64, bool) {
	var best string
	var bestConf float64
	found := false
	for _, td := range topicOrder {
		if ans, ok := answers[td.Question]; ok {
			if !found || ans.Confidence > bestConf {
				best = td.Question
				bestConf = ans.Confidence
				found = true
			}
		}
	}
	return best, bestConf, found
}

// maxTopicConfidenceFiltered returns the highest confidence among topics that
// are affirmative (choice A, case-insensitive, trimmed) and meet the threshold.
// It is used for the junk margin gate.
func maxTopicConfidenceFiltered(answers laya.Answers, threshold float64) (float64, bool) {
	var best float64
	found := false
	for _, td := range topicOrder {
		if ans, ok := answers[td.Question]; ok && isYes(ans, threshold) {
			if !found || ans.Confidence > best {
				best = ans.Confidence
				found = true
			}
		}
	}
	return best, found
}

// Summarize returns a one-line human-readable summary of an Action, useful
// for dry-run output. It is optional and has no effect on Decide logic.
func Summarize(a Action) string {
	switch a.Kind {
	case KindTrash:
		return fmt.Sprintf("TRASH (%s)", a.Reason)
	case KindLabel:
		return fmt.Sprintf("LABEL %s (%s)", strings.Join(a.Labels, ", "), a.Reason)
	case KindUnclassified:
		label := ""
		if len(a.Labels) > 0 {
			label = a.Labels[0]
		}
		return fmt.Sprintf("UNCLASSIFIED -> %s (%s)", label, a.Reason)
	default:
		return fmt.Sprintf("%s (%s)", a.Kind, a.Reason)
	}
}
