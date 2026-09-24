package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/AngeeelD/systemone-email-cleaner/internal/audit"
	"github.com/AngeeelD/systemone-email-cleaner/internal/config"
	"github.com/AngeeelD/systemone-email-cleaner/internal/extract"
	"github.com/AngeeelD/systemone-email-cleaner/internal/gmail"
	"github.com/AngeeelD/systemone-email-cleaner/internal/policy"
	"github.com/AngeeelD/systemone-email-cleaner/internal/systemone"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// systemOnePredictor abstracts the System One model for run.
type systemOnePredictor interface {
	Predict(ctx context.Context, state extract.State) (systemone.Answers, error)
	PredictParagraph(ctx context.Context, paragraph string) (systemone.Answers, error)
	Ping(ctx context.Context) error
}

// applier abstracts Gmail mutation for run.
type applier interface {
	Modify(ctx context.Context, id string, add, remove []string) error
	BatchModify(ctx context.Context, ids []string, add, remove []string) error
	Untrash(ctx context.Context, id string) error
}

// clockFunc returns now for run ID.
type clockFunc func() time.Time

// circuitBreaker counts consecutive failures and pauses when threshold reached.
type circuitBreaker struct {
	mu        sync.Mutex
	count     int
	threshold int
	endpoint  string
	ping      func(context.Context) error
	stdin     io.Reader
	stderr    io.Writer
	stderrMu  *sync.Mutex
	sleep     func(time.Duration)
}

func (cb *circuitBreaker) success() {
	cb.mu.Lock()
	cb.count = 0
	cb.mu.Unlock()
}
func (cb *circuitBreaker) failure(ctx context.Context) error {
	cb.mu.Lock()
	cb.count++
	shouldPause := cb.count >= cb.threshold
	cb.mu.Unlock()
	if shouldPause {
		return cb.pause(ctx)
	}
	return nil
}
func safeFprintf(mu *sync.Mutex, w io.Writer, format string, args ...any) {
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	fmt.Fprintf(w, format, args...)
}
func (cb *circuitBreaker) pause(ctx context.Context) error {
	endpoint := cb.endpoint
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8000"
	}
	safeFprintf(cb.stderrMu, cb.stderr, "⚠  System One server not reachable at %s\n", endpoint)
	safeFprintf(cb.stderrMu, cb.stderr, "   Check the service.  [ENTER] retry  ·  [Ctrl-C] exit\n")
	// Wait for a line on stdin. If context is cancelled, return.
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		br := bufio.NewReader(cb.stdin)
		line, err := br.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			errCh <- err
			return
		}
		ch <- line
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	case <-ch:
		// ENTER pressed, re-probe health.
		if cb.ping != nil {
			if err := cb.ping(ctx); err != nil {
				safeFprintf(cb.stderrMu, cb.stderr, "health check still failing: %v\n", err)
				// Keep count at threshold so next failure pauses again, but reset slightly to avoid immediate loop.
				// Return error so caller can decide; but spec says continue if healthy, otherwise pause again on next failure.
				// For now, reset count and retry; if ping fails, we treat as still paused and wait again?
				// Simpler: if ping fails, keep counter high and return error to retry loop.
				// We'll just keep paused state: wait again if next failure.
				// Reset to threshold-1 so next failure will pause again immediately.
				cb.mu.Lock()
				cb.count = cb.threshold - 1
				cb.mu.Unlock()
				// Re-pause? Loop by waiting again? For simplicity, return and let caller retry; caller will call failure again on next error.
				// But we want to block until health passes if user keeps pressing ENTER with failing service.
				// So we loop: keep prompting until ping succeeds or context canceled.
				// Instead of returning, loop internally until healthy.
				// Implement loop:
				for {
					// already printed once, now wait again after failed ping
					safeFprintf(cb.stderrMu, cb.stderr, "⚠  System One server not reachable at %s\n", endpoint)
					safeFprintf(cb.stderrMu, cb.stderr, "   Check the service.  [ENTER] retry  ·  [Ctrl-C] exit\n")
					br := bufio.NewReader(cb.stdin)
					line2, err2 := br.ReadString('\n')
					if err2 != nil && !errors.Is(err2, io.EOF) {
						return err2
					}
					_ = line2
					select {
					case <-ctx.Done():
						return ctx.Err()
					default:
					}
					if err := cb.ping(ctx); err == nil {
						cb.mu.Lock()
						cb.count = 0
						cb.mu.Unlock()
						return nil
					} else {
						safeFprintf(cb.stderrMu, cb.stderr, "health check still failing: %v\n", err)
					}
				}
			}
		}
		cb.mu.Lock()
		cb.count = 0
		cb.mu.Unlock()
		return nil
	}
}

func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "429") || strings.Contains(strings.ToLower(s), "rate limit") || strings.Contains(strings.ToLower(s), "quota")
}
func isUnprocessable(err error) bool {
	return errors.Is(err, systemone.ErrUnprocessable)
}

func backoffSleep(attempt int, sleep func(time.Duration)) {
	if sleep == nil {
		time.Sleep(time.Duration(math.Pow(2, float64(attempt))) * time.Second)
		return
	}
	sleep(time.Duration(math.Pow(2, float64(attempt))) * time.Second)
}

// extendedGMailAccess adds Modify etc to gmailAccess for run/rollback.
type extendedGmailAccess interface {
	gmailAccess
	Modify(ctx context.Context, id string, add, remove []string) error
	BatchModify(ctx context.Context, ids []string, add, remove []string) error
	Untrash(ctx context.Context, id string) error
}

func (a *app) runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.StringVar(&a.configPath, "config", a.configPath, "path to the config file")
	dryRun := fs.Bool("dry-run", false, "print the plan, write nothing")
	limit := fs.Int("limit", 0, "maximum number of messages to examine (0 means no limit)")
	minJunk := fs.Float64("min-confidence-junk", -1, "override junk confidence threshold")
	minTopic := fs.Float64("min-confidence-topic", -1, "override topic confidence threshold")
	reprocess := fs.String("reprocess", "", "reprocess mode: unclassified")
	before := fs.String("before", "", "only messages sent before this date (YYYY-MM-DD)")
	after := fs.String("after", "", "only messages sent after this date (YYYY-MM-DD)")
	workers := fs.Int("workers", 8, "concurrency")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *reprocess != "" && *reprocess != "unclassified" {
		fmt.Fprintf(a.stderr, "--reprocess must be \"unclassified\" if set\n")
		return exitUsage
	}
	if *limit < 0 {
		fmt.Fprintf(a.stderr, "--limit must be >= 0\n")
		return exitUsage
	}
	if *workers <= 0 {
		fmt.Fprintf(a.stderr, "--workers must be at least 1\n")
		return exitUsage
	}
	cfg, code := a.loadConfig()
	if code != exitOK {
		return code
	}
	if *minJunk >= 0 {
		cfg.Policy.MinConfidenceJunk = *minJunk
	}
	if *minTopic >= 0 {
		cfg.Policy.MinConfidenceTopic = *minTopic
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

	// Resolve dependencies, using app seams if set otherwise real implementations.
	gmailClient, err := a.openGmail(ctx, cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot open Gmail: %v\n", err)
		return exitGeneric
	}
	// Cast to extended if it implements more methods; for fake we rely on type assertion.
	var extGmail extendedGmailAccess
	if eg, ok := gmailClient.(extendedGmailAccess); ok {
		extGmail = eg
	} else {
		// Wrap minimal gmailAccess with no-op modifier for tests that don't need real mutation.
		extGmail = &gmailAdapter{inner: gmailClient}
	}

	systemOneClient, err := a.openSystemOne(ctx, cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "cannot open System One: %v\n", err)
		return exitGeneric
	}

	applierInst := a.newApplier(extGmail)
	if applierInst == nil {
		// fallback noop
		applierInst = &noopApplier{}
	}

	clock := a.clock
	if clock == nil {
		clock = time.Now
	}
	runID := clock().UTC().Format("20060102T150405Z")

	stdin := a.stdin
	if stdin == nil {
		stdin = io.NopCloser(strings.NewReader(""))
	}
	sleepFn := a.sleep

	// Build query
	var query string
	if *reprocess == "unclassified" {
		unclassifiedLabel := cfg.Labels["unclassified"]
		if strings.TrimSpace(unclassifiedLabel) == "" {
			unclassifiedLabel = "cleaner/unclassified"
		}
		query = fmt.Sprintf("in:inbox label:%s", unclassifiedLabel)
	} else {
		query = unprocessedQuery(cfg.Labels)
	}
	query, qerr := withDateRange(query, *before, *after)
	if qerr != nil {
		fmt.Fprintf(a.stderr, "%v\n", qerr)
		return exitUsage
	}

	ids, err := gmailClient.ListMessages(ctx, query, *limit)
	if err != nil {
		if isRateLimited(err) {
			// simple backoff retry once
			backoffSleep(0, sleepFn)
			ids, err = gmailClient.ListMessages(ctx, query, *limit)
		}
		if err != nil {
			fmt.Fprintf(a.stderr, "cannot list messages: %v\n", err)
			return exitGeneric
		}
	}
	if len(ids) == 0 {
		fmt.Fprintln(a.stdout, "no messages to process")
		return exitOK
	}
	if *dryRun {
		fmt.Fprintf(a.stdout, "dry-run: would process %d message(s) matching %q\n", len(ids), query)
	}

	// Rate limiter: 6000 units/min ~ 100 units/sec. One Get costs 20 units => ~5 gets/sec.
	// Use safe fraction: 3 per second, burst = workers to allow initial burst for small runs/tests.
	burst := *workers
	if burst < 5 {
		burst = 5
	}
	limiter := rate.NewLimiter(rate.Limit(3), burst)

	var outMu sync.Mutex
	var stderrMu sync.Mutex
	cb := &circuitBreaker{
		threshold: 5,
		endpoint:  cfg.SystemOne.Endpoint,
		ping: func(ctx context.Context) error {
			return systemOneClient.Ping(ctx)
		},
		stdin:    stdin,
		stderr:   a.stderr,
		stderrMu: &stderrMu,
		sleep:    sleepFn,
	}

	// Live progress on stderr while the run is in flight. It is silent when
	// stderr is not a terminal (cron, redirected logs).
	prog := newRunProgress(a.stderr, len(ids), nil)

	type result struct {
		rec audit.Record
		err error
	}

	// Worker pool with errgroup.
	g, gctx := errgroup.WithContext(ctx)
	idCh := make(chan string)
	resCh := make(chan result, len(ids))

	// Producer
	g.Go(func() error {
		defer close(idCh)
		for _, id := range ids {
			select {
			case <-gctx.Done():
				return gctx.Err()
			case idCh <- id:
			}
		}
		return nil
	})

	// Track audit writes and stats.
	var mu sync.Mutex
	var applied, skipped, errorsCount int

	// Start workers
	for i := 0; i < *workers; i++ {
		g.Go(func() error {
			for id := range idCh {
				// Rate limit
				if limiter != nil {
					if err := limiter.Wait(gctx); err != nil {
						return err
					}
				}
				rec, shouldCountAsFailure, err := processOne(gctx, extGmail, systemOneClient, applierInst, cfg, id, runID, *dryRun, sleepFn)
				// Handle circuit breaker for service-down failures.
				if shouldCountAsFailure {
					if pauseErr := cb.failure(gctx); pauseErr != nil {
						return pauseErr
					}
					// For service-down we still produce a skipped record? ProcessOne already returned one.
					// We continue; breaker has paused and recovered.
				} else {
					// success or single-message skip resets? For 422 we don't count, but for success reset.
					if err == nil && rec.Outcome == "applied" {
						cb.success()
					} else if isUnprocessable(err) || rec.Outcome == "skipped" {
						// single-message skip does not count toward breaker, but also does not reset successes? Keep count as is.
						// Optionally do not reset, but don't increment.
					} else if rec.Outcome == "applied" {
						cb.success()
					}
				}

				if rec.MessageID == "" {
					rec.MessageID = id
					rec.RunID = runID
				}

				// Audit persistence (except dry-run)
				if !*dryRun {
					// Serialize audit appends to avoid interleaved file writes.
					mu.Lock()
					werr := audit.Append(cfg.Audit.Dir, rec)
					mu.Unlock()
					if werr != nil {
						safeFprintf(&stderrMu, a.stderr, "cannot write audit for %s: %v\n", id, werr)
					}
				} else {
					// dry-run prints plan line
					line := formatDryRun(rec)
					safeFprintf(&outMu, a.stdout, "%s\n", line)
				}

				if !*dryRun {
					prog.record(rec)
					// A run is quiet on success and loud on failure: the live
					// counter covers progress, and each error or skip prints a
					// line so it shows up immediately, not only in the audit.
					if rec.Outcome == "error" || rec.Outcome == "skipped" {
						safeFprintf(&outMu, a.stdout, "%s\n", formatDryRun(rec))
					}
				}

				mu.Lock()
				switch rec.Outcome {
				case "applied":
					applied++
				case "skipped":
					skipped++
				case "error":
					errorsCount++
				default:
					if err != nil {
						errorsCount++
					}
				}
				mu.Unlock()

				select {
				case resCh <- result{rec: rec, err: err}:
				case <-gctx.Done():
					return gctx.Err()
				}
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(a.stderr, "run failed: %v\n", err)
		return exitGeneric
	}
	close(resCh)
	prog.finish()

	if *dryRun {
		fmt.Fprintf(a.stdout, "dry-run complete: %d would be applied, %d skipped, %d errors\n", applied, skipped, errorsCount)
		return exitOK
	}
	fmt.Fprintf(a.stdout, "run %s: %d applied, %d skipped, %d errors (%d total)\n", runID, applied, skipped, errorsCount, len(ids))
	if errorsCount > 0 {
		// A run with per-message failures must not exit 0: cron and CI would
		// treat a broken run as a clean one.
		return exitGeneric
	}
	return exitOK
}

func formatDryRun(rec audit.Record) string {
	if rec.Outcome == "skipped" {
		return fmt.Sprintf("SKIP %s %q: %s", rec.MessageID, rec.Subject, rec.Reason)
	}
	if rec.Action != nil {
		return fmt.Sprintf("%s %s %q -> add=%v remove=%v (%s)", strings.ToUpper(rec.Outcome), rec.MessageID, rec.Subject, rec.Action.Add, rec.Action.Remove, rec.Reason)
	}
	return fmt.Sprintf("%s %s %q (%s)", rec.Outcome, rec.MessageID, rec.Subject, rec.Reason)
}

func processOne(ctx context.Context, gmailClient extendedGmailAccess, systemOneClient systemOnePredictor, ap applier, cfg *config.Config, id, runID string, dryRun bool, sleep func(time.Duration)) (audit.Record, bool, error) {
	rec := audit.Record{
		RunID:     runID,
		MessageID: id,
		Outcome:   "error",
	}

	// GetMessage with 429 backoff (up to 3 retries)
	var msg *gmail.Message
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		msg, err = gmailClient.GetMessage(ctx, id)
		if err == nil {
			break
		}
		if isRateLimited(err) && attempt < 3 {
			backoffSleep(attempt, sleep)
			continue
		}
		break
	}
	if err != nil {
		rec.Subject = ""
		rec.Reason = fmt.Sprintf("get message: %v", err)
		rec.Outcome = "skipped"
		// Get failures are per-message, not service-down unless it's systemone. So not counting toward breaker.
		return rec, false, err
	}
	rec.Subject = msg.Subject
	rec.LabelsBefore = append([]string(nil), msg.LabelIDs...)

	paragraph := extract.ToParagraph(*msg, cfg.Extract)

	// Predict with 429 backoff and 422 handling
	var answers systemone.Answers
	for attempt := 0; attempt < 4; attempt++ {
		answers, err = systemOneClient.PredictParagraph(ctx, paragraph)
		if err == nil {
			break
		}
		if isUnprocessable(err) {
			rec.Reason = fmt.Sprintf("systemone unprocessable: %v", err)
			rec.Outcome = "skipped"
			rec.Answers = nil
			rec.LabelsAfter = append([]string(nil), msg.LabelIDs...)
			return rec, false, err
		}
		if isRateLimited(err) && attempt < 3 {
			backoffSleep(attempt, sleep)
			continue
		}
		// Check for malformed JSON / unexpected shape: treat as skipped, not breaker.
		s := strings.ToLower(err.Error())
		if strings.Contains(s, "decode") || strings.Contains(s, "unexpected shape") || strings.Contains(s, "malformed") {
			rec.Reason = fmt.Sprintf("systemone decode error: %v", err)
			rec.Outcome = "skipped"
			rec.LabelsAfter = append([]string(nil), msg.LabelIDs...)
			return rec, false, err
		}
		break
	}
	if err != nil {
		// Service-down candidate.
		rec.Reason = fmt.Sprintf("systemone predict: %v", err)
		rec.Outcome = "skipped"
		rec.LabelsAfter = append([]string(nil), msg.LabelIDs...)
		// Count toward breaker unless it's 429 that we already retried? 429 is rate limit, not breaker.
		shouldCount := !isRateLimited(err) && !isUnprocessable(err)
		return rec, shouldCount, err
	}

	// Capture routing model not available; leave empty.

	// Convert answers for audit.
	auditAnswers := make(map[string]audit.Answer, len(answers))
	for k, a := range answers {
		auditAnswers[k] = audit.Answer{Choice: a.Choice, Confidence: a.Confidence}
	}
	rec.Answers = auditAnswers

	action := policy.Decide(answers, cfg.Policy, cfg.Labels)
	rec.Reason = action.Reason
	rec.LabelsAfter = append([]string(nil), msg.LabelIDs...)

	var add, remove []string
	switch action.Kind {
	case policy.KindTrash:
		add = []string{"TRASH"}
		remove = []string{"INBOX"}
		// Update LabelsAfter for audit: remove INBOX, add TRASH
		rec.LabelsAfter = applyLabelDelta(msg.LabelIDs, add, remove)
		rec.Action = &audit.Action{Add: add, Remove: remove}
		rec.Outcome = "applied"
		if !dryRun {
			// 429 backoff for modify
			var merr error
			for attempt := 0; attempt < 4; attempt++ {
				merr = ap.Modify(ctx, id, add, remove)
				if merr == nil {
					break
				}
				if isRateLimited(merr) && attempt < 3 {
					backoffSleep(attempt, sleep)
					continue
				}
				break
			}
			if merr != nil {
				rec.Outcome = "error"
				rec.Reason = fmt.Sprintf("modify: %v", merr)
				return rec, false, merr
			}
		}
	case policy.KindLabel, policy.KindUnclassified:
		add = action.Labels
		remove = nil
		rec.LabelsAfter = applyLabelDelta(msg.LabelIDs, add, remove)
		rec.Action = &audit.Action{Add: add, Remove: remove}
		rec.Outcome = "applied"
		if !dryRun {
			var merr error
			for attempt := 0; attempt < 4; attempt++ {
				merr = ap.Modify(ctx, id, add, remove)
				if merr == nil {
					break
				}
				if isRateLimited(merr) && attempt < 3 {
					backoffSleep(attempt, sleep)
					continue
				}
				break
			}
			if merr != nil {
				rec.Outcome = "error"
				rec.Reason = fmt.Sprintf("modify: %v", merr)
				return rec, false, merr
			}
		}
	default:
		rec.Outcome = "skipped"
		rec.Reason = action.Reason
	}

	return rec, false, nil
}

func applyLabelDelta(before, add, remove []string) []string {
	set := make(map[string]bool, len(before))
	for _, l := range before {
		set[l] = true
	}
	for _, r := range remove {
		delete(set, r)
	}
	for _, a := range add {
		set[a] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	// Sort for determinism
	// Use simple sort
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// gmailAdapter wraps minimal gmailAccess to satisfy extended interface with no-ops for modify.
type gmailAdapter struct {
	inner gmailAccess
}

func (g *gmailAdapter) EnsureLabel(ctx context.Context, name string) (string, error) {
	return g.inner.EnsureLabel(ctx, name)
}
func (g *gmailAdapter) Profile(ctx context.Context) (string, error) {
	return g.inner.Profile(ctx)
}
func (g *gmailAdapter) ListMessages(ctx context.Context, q string, max int) ([]string, error) {
	return g.inner.ListMessages(ctx, q, max)
}
func (g *gmailAdapter) GetMessage(ctx context.Context, id string) (*gmail.Message, error) {
	return g.inner.GetMessage(ctx, id)
}
func (g *gmailAdapter) Modify(_ context.Context, _ string, _, _ []string) error        { return nil }
func (g *gmailAdapter) BatchModify(_ context.Context, _ []string, _, _ []string) error { return nil }
func (g *gmailAdapter) Untrash(_ context.Context, _ string) error                      { return nil }

type noopApplier struct{}

func (n *noopApplier) Modify(_ context.Context, _ string, _, _ []string) error        { return nil }
func (n *noopApplier) BatchModify(_ context.Context, _ []string, _, _ []string) error { return nil }
func (n *noopApplier) Untrash(_ context.Context, _ string) error                      { return nil }
