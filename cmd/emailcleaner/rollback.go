package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/AngeeelD/systemone-email-cleaner/internal/audit"
)

func (a *app) rollbackCmd(args []string) int {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.StringVar(&a.configPath, "config", a.configPath, "path to the config file")
	runID := fs.String("run-id", "", "run ID to rollback (default: latest)")
	if err := fs.Parse(args); err != nil {
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
	client, err := a.openGmail(ctx, cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot open Gmail: %v\n", err)
		return exitGeneric
	}
	var extGmail extendedGmailAccess
	if eg, ok := client.(extendedGmailAccess); ok {
		extGmail = eg
	} else {
		extGmail = &gmailAdapter{inner: client}
	}
	applierInst := a.newApplier(extGmail)
	if applierInst == nil {
		applierInst = &noopApplier{}
	}

	dir := cfg.Audit.Dir
	if strings.TrimSpace(dir) == "" {
		dir = "audit"
	}

	var recs []audit.Record
	var targetRun string
	if *runID != "" {
		targetRun = *runID
		recs, err = audit.Read(dir, targetRun)
		if err != nil {
			fmt.Fprintf(a.stderr, "cannot read audit for run %s: %v\n", targetRun, err)
			return exitGeneric
		}
	} else {
		recs, targetRun, err = audit.LoadLatest(dir)
		if err != nil {
			fmt.Fprintf(a.stderr, "cannot load latest audit: %v\n", err)
			return exitGeneric
		}
	}
	if len(recs) == 0 {
		fmt.Fprintf(a.stdout, "no records for run %s\n", targetRun)
		return exitOK
	}

	// Build plan with verification.
	type planItem struct {
		rec          audit.Record
		actionAdd    []string
		actionRemove []string
		useUntrash   bool
		skipReason   string
		shouldSkip   bool
	}
	var plan []planItem
	for _, rec := range recs {
		if rec.Outcome != "applied" || rec.Action == nil {
			plan = append(plan, planItem{rec: rec, shouldSkip: true, skipReason: "not applied"})
			continue
		}
		// Verify before revert: check current labels.
		msg, err := extGmail.GetMessage(ctx, rec.MessageID)
		if err != nil {
			plan = append(plan, planItem{rec: rec, shouldSkip: true, skipReason: fmt.Sprintf("cannot fetch message: %v", err)})
			continue
		}
		ok, reason := audit.ShouldRevert(rec, msg.LabelIDs)
		if !ok {
			plan = append(plan, planItem{rec: rec, shouldSkip: true, skipReason: reason})
			continue
		}
		// Invert action.
		var add, remove []string
		var useUntrash bool
		// Trash case: original added TRASH, removed INBOX. Invert is untrash (remove TRASH, add INBOX).
		// Detect trash by checking if Action.Add contains TRASH.
		isTrash := false
		for _, a := range rec.Action.Add {
			if a == "TRASH" {
				isTrash = true
				break
			}
		}
		if isTrash {
			useUntrash = true
			// For untrash we still record inverted labels via dedicated call.
			add = nil
			remove = nil
		} else {
			// Invert add/remove.
			add = rec.Action.Remove
			remove = rec.Action.Add
		}
		plan = append(plan, planItem{rec: rec, actionAdd: add, actionRemove: remove, useUntrash: useUntrash})
	}

	// Print plan
	fmt.Fprintf(a.stdout, "rollback plan for run %s: %d record(s)\n", targetRun, len(plan))
	var toApply []planItem
	var toSkip []planItem
	for _, p := range plan {
		if p.shouldSkip {
			toSkip = append(toSkip, p)
			fmt.Fprintf(a.stdout, "  SKIP %s %q: %s\n", p.rec.MessageID, p.rec.Subject, p.skipReason)
		} else {
			toApply = append(toApply, p)
			if p.useUntrash {
				fmt.Fprintf(a.stdout, "  UNTRASH %s %q (invert TRASH)\n", p.rec.MessageID, p.rec.Subject)
			} else {
				fmt.Fprintf(a.stdout, "  REVERT %s %q: add=%v remove=%v\n", p.rec.MessageID, p.rec.Subject, p.actionAdd, p.actionRemove)
			}
		}
	}
	if len(toApply) == 0 {
		fmt.Fprintln(a.stdout, "nothing to revert")
		return exitOK
	}
	fmt.Fprintf(a.stdout, "\nPress [ENTER] to revert %d message(s), [Ctrl-C] to cancel\n", len(toApply))

	reader := a.stdin
	if reader == nil {
		// No stdin available, assume ENTER (tests always set stdin).
	} else {
		br := bufio.NewReader(reader)
		_, _ = br.ReadString('\n')
		// Check context? Just proceed after ENTER. Ctrl-C would cancel context via signal, not here.
		select {
		case <-ctx.Done():
			fmt.Fprintln(a.stderr, "rollback canceled")
			return exitGeneric
		default:
		}
	}

	// Apply reverts.
	var applied, skipped int
	for _, p := range toApply {
		var err error
		if p.useUntrash {
			err = applierInst.Untrash(ctx, p.rec.MessageID)
		} else {
			err = applierInst.Modify(ctx, p.rec.MessageID, p.actionAdd, p.actionRemove)
		}
		if err != nil {
			fmt.Fprintf(a.stderr, "failed to revert %s: %v\n", p.rec.MessageID, err)
			skipped++
			continue
		}
		applied++
	}
	skipped += len(toSkip)
	fmt.Fprintf(a.stdout, "rollback %s: %d reverted, %d skipped\n", targetRun, applied, skipped)
	_ = io.Discard // keep import
	return exitOK
}
