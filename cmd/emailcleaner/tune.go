package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/AngeeelD/systemone-email-cleaner/internal/config"
	"github.com/AngeeelD/systemone-email-cleaner/internal/extract"
	"github.com/AngeeelD/systemone-email-cleaner/internal/policy"
	"github.com/AngeeelD/systemone-email-cleaner/internal/systemone"
)

// tune samples messages once, asks the model once per message, then replays the
// policy across a grid of thresholds. It writes nothing: no labels change, no
// audit file is created. It exists so the thresholds are chosen from data rather
// than guesswork.
func (a *app) tune(args []string) int {
	fs := flag.NewFlagSet("tune", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.StringVar(&a.configPath, "config", a.configPath, "path to the config file")
	limit := fs.Int("limit", 100, "number of messages to sample (at least 1)")
	before := fs.String("before", "", "only messages sent before this date (YYYY-MM-DD)")
	after := fs.String("after", "", "only messages sent after this date (YYYY-MM-DD)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *limit <= 0 {
		fmt.Fprintf(a.stderr, "--limit must be at least 1\n")
		return exitUsage
	}

	cfg, code := a.loadConfig()
	if code != exitOK {
		return code
	}
	ctx := context.Background()

	if err := a.checkToken(ctx, cfg); err != nil {
		if isReAuth(err) {
			fmt.Fprintf(a.stderr, "gmail token expired or revoked; run `emailcleaner setup`\n")
			return exitReAuth
		}
		fmt.Fprintf(a.stderr, "token check failed: %v\n", err)
		return exitGeneric
	}

	gmailClient, err := a.openGmail(ctx, cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot open Gmail: %v\n", err)
		return exitGeneric
	}
	modelClient, err := a.openSystemOne(ctx, cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot open System One: %v\n", err)
		return exitGeneric
	}

	query, qerr := withDateRange(unprocessedQuery(cfg.Labels), *before, *after)
	if qerr != nil {
		fmt.Fprintf(a.stderr, "%v\n", qerr)
		return exitUsage
	}
	ids, err := gmailClient.ListMessages(ctx, query, *limit)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot list messages: %v\n", err)
		return exitGeneric
	}
	if len(ids) == 0 {
		fmt.Fprintln(a.stdout, "no messages to sample")
		return exitOK
	}

	tty := isTerminal(a.stderr)
	var samples []systemone.Answers
	skipped := 0
	for i, id := range ids {
		if tty {
			fmt.Fprintf(a.stderr, "\r\033[K  sampling %d/%d", i+1, len(ids))
		}
		msg, err := gmailClient.GetMessage(ctx, id)
		if err != nil {
			skipped++
			continue
		}
		answers, err := modelClient.PredictParagraph(ctx, extract.ToParagraph(*msg, cfg.Extract))
		if err != nil {
			skipped++
			continue
		}
		samples = append(samples, answers)
	}
	if tty {
		fmt.Fprint(a.stderr, "\r\033[K")
	}

	fmt.Fprintf(a.stdout, "sampled %d message(s) (%d skipped)\n%q\n\n", len(samples), skipped, query)
	printSweep(a.stdout, cfg, samples)
	return exitOK
}

// tuneJunkThresholds and tuneTopicThresholds are the grid the sweep walks.
var (
	tuneJunkThresholds  = []float64{0.45, 0.50, 0.55, 0.60, 0.65}
	tuneTopicThresholds = []float64{0.55, 0.60, 0.65}
)

// printSweep replays the policy for every threshold pair and prints how the
// sampled messages would be divided. The current config is marked with "*".
func printSweep(w io.Writer, cfg *config.Config, samples []systemone.Answers) {
	fmt.Fprintf(w, "  junk  topic |  trash  label  unclass  spillover\n")
	fmt.Fprintf(w, "  -----------+-------------------------------------\n")
	for _, j := range tuneJunkThresholds {
		for _, t := range tuneTopicThresholds {
			p := config.Policy{MinConfidenceJunk: j, MinConfidenceTopic: t}
			trash, label, unclass, spill := sweepCounts(samples, p, cfg.Labels)
			mark := " "
			if j == cfg.Policy.MinConfidenceJunk && t == cfg.Policy.MinConfidenceTopic {
				mark = "*"
			}
			fmt.Fprintf(w, "%s %5.2f  %5.2f |  %5d  %5d  %7d  %9d\n", mark, j, t, trash, label, unclass, spill)
		}
	}
	fmt.Fprintf(w, "\n(* = current config)\n")
}

// sweepCounts replays Decide over samples and tallies the outcomes.
func sweepCounts(samples []systemone.Answers, p config.Policy, labels map[string]string) (trash, label, unclass, spill int) {
	for _, ans := range samples {
		switch action := policy.Decide(ans, p, labels); action.Kind {
		case policy.KindTrash:
			trash++
		case policy.KindUnclassified:
			unclass++
		default:
			label++
			if len(action.Labels) > 1 {
				spill++
			}
		}
	}
	return trash, label, unclass, spill
}
