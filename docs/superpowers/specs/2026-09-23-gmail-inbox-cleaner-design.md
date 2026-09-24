# Gmail Inbox Cleaner — Design

**Date:** 2026-09-23
**Status:** Approved
**Author:** Design session (orchestrator + user)

## Problem

The user's Gmail inbox is unmanageable. Mail that matters (people, actionable
requests, account security, purchases, opportunities) is buried under bulk mail,
and nothing is grouped. Manually triaging thousands of messages is the same work
as never starting.

## Goal

A local Go tool that reads inbox **metadata and a body preview**, asks a hosted
[Laya](https://huggingface.co/convaiinnovations/laya) decision model to classify
each message, and applies Gmail labels — moving genuine junk to Trash — so the
inbox becomes grouped and reviewable.

## Non-Goals

- **No permanent deletion.** `messages.delete` and the `https://mail.google.com/`
  scope are deliberately out of scope. "Delete" always means Trash (recoverable
  for 30 days).
- **No archiving of important mail.** The five useful topics stay in `INBOX`.
  Only junk loses `INBOX`.
- No web UI, no daemon, no Pub/Sub. This is a script run on demand or by cron.
- No reply generation, no sending, no drafts.
- Not published as a public OAuth app.

## Confirmed Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Mailbox access | Gmail API + OAuth2 | Native labels, better metadata than IMAP |
| Autonomy | Auto with undo window | Labels + Trash applied automatically; full audit log + rollback |
| State model | `A` — stateless labeler, no database | Gmail labels are the source of truth; audit is append-only JSONL |
| Importance | All five topics grouped, never archived | User wants groups, not a hidden inbox |
| Junk | Removed from view (Trash) | The only destructive-class action, gated hardest |
| Low confidence | `cleaner/unclassified` label | Manual review later, never a guess |
| Per-message failure | Skip + log, keep going | One bad message must not stop a run |
| Service down | Circuit breaker after 5 consecutive failures | Pause with instructions, resume from the same point |
| Deployment | Local script, Testing-mode OAuth | Not published; 7-day token expiry is accepted and handled |

## Architecture

### Package layout

Dependencies flow in one direction only. `policy` is pure — no network, no I/O —
and is therefore the most heavily tested package, because it is the only place
that can lose a message.

```
cmd/emailcleaner/       CLI: flags, wiring, exit codes
internal/gmail/         OAuth2 + Gmail API: List, Get, BatchModify, label setup
internal/extract/       Message -> State (HTML strip, quote/signature trim, budget)
internal/laya/          HTTP client for laya-serve + question/answer types
internal/policy/        Laya answers -> Action   (PURE: no network, no I/O)
internal/act/           Applies actions to Gmail + writes audit.jsonl
internal/audit/         audit.jsonl: append, replay, rollback
internal/config/        YAML config loading and defaults
```

### Data flow (per message)

1. `gmail.List` → message IDs matching `in:inbox -label:cleaner/*`, paged at 500.
2. Worker pool → `gmail.Get` (`format=full`, which returns headers *and* body).
3. `extract.State(msg)` → JSON sized to the model's token budget.
4. `laya.Predict(ctx, state, questions)` → `POST /v1/systemone`.
5. `policy.Decide(answers, msg)` → typed `Action` (`Label` / `Trash` / `Skip`) + reason.
6. `act.Apply` → `batchModify` labels, then append the audit record.

### Concurrency and Gmail quota

Both limits below are verified against Google's *Usage limits* documentation
(retrieved 2026-09-23). They are independent and both must be respected.

| Limit | Value |
|---|---|
| Per minute per project | 1,200,000 quota units |
| **Per minute per user per project** | **6,000 quota units** |
| Per day per project (billing threshold) | 80,000,000 quota units |

Per-method cost, for the methods this design uses:

| Method | Units |
|---|---|
| `messages.get` | **20** |
| `messages.list` | 5 |
| `messages.batchModify` | 50 (per call, up to 1000 message IDs) |
| `messages.modify` / `messages.trash` | 5 / 20 |
| `labels.list` / `labels.create` | 1 / 5 |
| `getProfile` | 1 |

**Capacity model.** One `messages.get` per email (20 units) is unavoidable and is
the entire per-message cost — `format=metadata` and `format=full` cost the same,
so there is no cheaper way to obtain headers, and one `full` call covers both
headers and body. Amortized `batchModify` adds ~0.05 units per message.

> 10,000 emails × ~20.05 units ≈ 200,500 units ≈ **~33 minutes minimum**
> at 6,000 units/minute.

The bottleneck is **Gmail quota, not Laya.** Laya answers every question for a
message in a single forward pass (~33–72 ms) and sustains 103–332 questions/sec
batched on a T4. Therefore:

- A worker pool (`errgroup`) with a `golang.org/x/time/rate` token bucket set to
  a safe fraction of 6,000 units/minute.
- 429 responses get exponential backoff. They are **not** a pause condition.
- `messages.list` can be paged aggressively; it costs 5 units per call.

## Laya Integration

### Verified model facts that shaped this design

| Fact | Source | Consequence |
|---|---|---|
| `laya-serve` exposes `POST /v1/systemone`, the Jev-compatible contract | model card | No inference layer to build; Go only needs an HTTP client |
| Every question in a call is answered in one forward pass | model card | 6 questions per email cost one round trip |
| `noul` can follow its own labels (`false:` / `true:`) instead of the state | issue #156 | **`noul` is not used at all** |
| Ordinal `score` is the weakest primitive (SST-5 0.372) | model card | Urgency scoring is out of scope |
| `action.act_probability` reads 1.0 almost always; AUROC 0.30 | issue #185 | Ignored. Gate on `confidence` (AUROC 0.77) |
| Ships over-confident; temperature refit moves ECE 0.466 → 0.081 | model card | Thresholds must be calibrated from data, not chosen a priori |
| `choice` degrades above ~20 options | model card | Largest question here has 2 options — limit avoided entirely |
| English root checkpoint: 512 ctx, `head_max_len` 192 (~320 tokens of state) | model card | Drives the extraction budget |
| `multilingual` checkpoint: mmBERT-base, 1024 ctx | model card | Required for Spanish-language mail |

### Serving

Run on the dedicated machine:

```bash
pip install "laya[serve]"
LAYA_DEVICE=cuda LAYA_PRELOAD=1 laya-serve    # 0.0.0.0:8000
```

`LAYA_PRELOAD=1` keeps both the English and multilingual checkpoints resident so
the router switches on language detection only (<1 ms) instead of reloading
(7–10 s per switch). Set `LAYA_API_KEY` to require
`Authorization: Bearer <key>`; the Go client reads the key from the environment
(`laya.api_key_env`), never from the config file.

### State schema

```go
type State struct {
    From           string   `json:"from"`
    FromDomain     string   `json:"from_domain"`
    Subject        string   `json:"subject"`
    Date           string   `json:"date"` // RFC3339
    Labels         []string `json:"labels,omitempty"`
    HasAttachments bool     `json:"has_attachments"`
    BodyPreview    string   `json:"body_preview"` // truncated to extract.body_preview_chars
}
```

### Questions

**Six independent binary questions in one request.** Two reasons:

1. **The topics overlap.** A message from a colleague asking for a deliverable is
   both `people` and `action`. A five-option `choice` softmaxes to a single
   answer; six independent binaries produce real multi-label output.
2. **A high-cardinality `choice` would hit the documented >20-option cliff.**
   The largest question here has two options.

Every question is a 2-option `choice` with **neutral keys** (`A`/`B`) and the
yes/no wording in the **descriptions** — the documented workaround for the
`noul` label-following defect.

```json
{
  "is_junk": {
    "type": "choice",
    "instructions": "Is this email unwanted bulk mail the recipient should not keep? Count as junk: marketing and promotions, newsletters the recipient did not sign up for, mass automated notifications, spam, phishing. Answer no if a real person wrote to the recipient, or if it may contain an invoice, an order, an account or security alert, or anything the recipient may need to act on. When unsure, answer no.",
    "criteria": { "A": "yes, unwanted bulk or junk mail", "B": "no, this is not junk" }
  },
  "is_person": {
    "type": "choice",
    "instructions": "Was this email written by a real human being addressing the recipient directly, as opposed to an automated system, mailing list, or bulk sender?",
    "criteria": { "A": "yes, written by a real person", "B": "no, automated or bulk" }
  },
  "needs_action": {
    "type": "choice",
    "instructions": "Does this email expect a response or an action from the recipient? Count: invoices to pay, contracts to sign, deadlines, questions asked directly. Do not count: informational notices, order confirmations the recipient only files, marketing.",
    "criteria": { "A": "yes, the recipient must act or reply", "B": "no action is required" }
  },
  "is_security": {
    "type": "choice",
    "instructions": "Is this an account or security notice from a service provider? Count: sign-in and access alerts, password changes, two-factor codes, banking and card transaction alerts, suspicious activity warnings, breach notifications.",
    "criteria": { "A": "yes, account or security notice", "B": "no, not a security notice" }
  },
  "is_purchase": {
    "type": "choice",
    "instructions": "Is this about a purchase or a delivery? Count: order confirmations, receipts, invoices for something bought, shipping and tracking updates, reservations, active subscription notices.",
    "criteria": { "A": "yes, purchase, receipt or delivery", "B": "no, not about a purchase" }
  },
  "is_opportunity": {
    "type": "choice",
    "instructions": "Is this a professional opportunity the recipient may want to act on? Count: recruiters and job offers, freelance or contract proposals, networking, collaboration requests, commercial leads. Answer no for mass job-alert digests unless they name the recipient for a specific role.",
    "criteria": { "A": "yes, a professional opportunity", "B": "no, not an opportunity" }
  }
}
```

The `is_junk` instructions deliberately bias toward "no" on uncertainty: a false
positive there sends important mail to Trash.

Response handling: read `answers.<name>.choice` plus its confidence, and record
`routing.model` in the audit log so it is possible to tell which checkpoint
answered a given message.

## Decision Policy

`internal/policy` is pure and deterministic, applied in this order:

1. `is_junk` = `A` **and** confidence ≥ `min_confidence_junk` → **Trash**. No
   other action is taken for that message.
2. For each topic answered `A` with confidence ≥ `min_confidence_topic` → **add**
   its label. Multiple labels may be applied to one message.
3. Not junk and no topic cleared its threshold → `cleaner/unclassified`.
4. **`INBOX` is never removed** except when the message goes to Trash.

### Idempotency

Presence of any `cleaner/*` label means "processed", so the run query
`in:inbox -label:cleaner/*` naturally skips it. A message is only "done" once it
carries a label, so a crashed run needs no pointer to resume from — the next run
re-lists what is unlabeled and continues.

`--reprocess=unclassified` re-evaluates messages carrying
`cleaner/unclassified`, for use after tuning thresholds or the taxonomy. By
default nothing is processed twice.

## Labels

| Label | Meaning |
|---|---|
| `cleaner/people` | A real person writing to the user |
| `cleaner/action` | Requires a reply or an action |
| `cleaner/security` | Account and security notices |
| `cleaner/accounts` | Purchases and deliveries |
| `cleaner/opportunities` | Professional opportunities |
| `cleaner/unclassified` | Below threshold — manual review |
| *(no label)* | Junk → Trash |

Label names live in the config file, not in code. `setup` creates any missing
labels (`labels.create`, 5 units each) and is idempotent. Gmail nests labels by
`/`, so creating `cleaner/people` also creates the `cleaner` parent.

## Audit Log and Rollback

One append-only file per run: `audit/<run-id>.jsonl`, one line per message.

```json
{"run_id":"20260923T132105Z","message_id":"18f1a…","subject":"Factura #4411",
 "outcome":"applied","reason":"is_junk conf=0.97","routing_model":"multilingual",
 "answers":{"is_junk":{"choice":"A","confidence":0.97}},
 "labels_before":["INBOX","UNREAD"],"labels_after":["UNREAD","TRASH"],
 "action":{"add":["TRASH"],"remove":["INBOX"]}}
```

- `outcome` is `applied`, `skipped`, or `error`. A `skipped` record carries the
  reason, satisfying the requirement that unprocessed mail is documented.
- Every confidence is recorded, so thresholds can be tuned from data.
- `labels_before` stores the complete prior label set, not a delta, so rollback
  is exact.

`rollback` (default: most recent run, or `--run-id`) inverts each `applied`
record. **It verifies before reverting:** if a message no longer carries the
labels this run set — because the user moved it manually — it is skipped and
reported rather than clobbered. It prints the plan and waits for ENTER, like
every other mutating operation. Reverting a Trash requires `messages.untrash`
(5 units per call).

## OAuth, Credentials and Deployment

`setup` runs the loopback flow (`http://127.0.0.1:<port>/callback`), which is how
Desktop-type OAuth clients work — no public URL or server needed. The token is
written to `token.json` with mode `0600`.

**Scope: `https://www.googleapis.com/auth/gmail.modify` only.** It covers
listing, reading, label modification and Trash. `https://mail.google.com/` is not
requested because it would enable permanent deletion, which is out of scope by
design.

### The 7-day token

Google's OAuth documentation states:

> A Google Cloud Platform project with an OAuth consent screen configured for an
> external user type and a publishing status of "Testing" is issued a refresh
> token expiring in 7 days.

Because this app stays unpublished, **`setup` is a weekly task.** Mitigations,
all required:

- `run` validates the token *before* starting and exits immediately with a clear
  message if it is dead.
- Exit code `3` means "re-authenticate" (distinct from `1` for generic errors) so
  a wrapper or cron job can tell the difference instead of failing silently.
- Other refresh-token invalidators to expect: changing the account password,
  six months unused, manual revocation, and Google's 100-refresh-token-per-client
  cap.

Publishing to Production would remove the 7-day rule (that rule is specific to
Testing) while leaving the app unverified and usable only by its owner; personal
use is an explicit exception to Google's verification requirement. The user
declined this; the design works either way and only the frequency of `setup`
changes.

`client_secret.json` and `token.json` must be gitignored.

## Configuration

```yaml
gmail:
  credentials_file: client_secret.json
  token_file: token.json
laya:
  endpoint: http://127.0.0.1:8000
  api_key_env: LAYA_API_KEY
  timeout: 30s
  workers: 8
policy:
  min_confidence_junk: 0.90
  min_confidence_topic: 0.70
labels:
  people: cleaner/people
  action: cleaner/action
  security: cleaner/security
  accounts: cleaner/accounts
  opportunities: cleaner/opportunities
  unclassified: cleaner/unclassified
extract:
  body_preview_chars: 800
audit:
  dir: audit
```

## CLI

```
emailcleaner setup       # OAuth + create missing labels. Idempotent.
emailcleaner status      # Token health + last run summary.
emailcleaner list        # Print the headers of unprocessed inbox messages. Read-only.
    --limit N                  examine at most N messages (at least 1)
emailcleaner run         # Classify and act.
    --dry-run                  print the plan, write nothing
    --limit N                  examine at most N messages
    --min-confidence-junk F    override threshold
    --min-confidence-topic F   override threshold
    --reprocess=unclassified   re-evaluate unclassified messages
    --workers N                concurrency
emailcleaner rollback    # Undo a run (default: the latest).
    --run-id X
```

`list` is a permanent debugging aid, not a milestone-1 stopgap: it prints
`date  from-domain  subject` for each unprocessed inbox message and changes
nothing, so it stays useful for inspecting what a run would see.

## Error Handling, Pause and Resume

Two failure classes are handled differently. Pausing on every bad message would
make the tool unusable.

| Class | Detection | Behaviour |
|---|---|---|
| Single message | Laya 422, malformed JSON, unexpected shape | Message left untouched, `outcome: skipped` + reason in the audit log, pool continues. **No pause.** |
| Service down | 5 consecutive failures, or health check fails | **Circuit breaker.** Pause. |
| Token expired | `invalid_grant` on refresh, checked before the run | Pause with the re-auth instruction. Exit code 3. |
| Quota / 429 | HTTP 429 | Exponential backoff. **No pause.** |

The pause prompt:

```
⚠  laya-serve not reachable at http://<host>:8000
   Check the service.  [ENTER] retry  ·  [Ctrl-C] exit
```

ENTER re-probes the health endpoint and continues from the same point if healthy.

## Extraction Budget

`body_preview_chars` defaults to 800. The English checkpoint provides ~320 tokens
of state budget (512 context − 192 `head_max_len`); JSON headers consume roughly
100 of those, leaving ~200 tokens, which is roughly 800 characters. The value is
configurable, and the dry run will show whether signal is being clipped.

`internal/extract` performs HTML → text, drops quoted replies (leading `>`,
`El … escribió:`, `On … wrote:`), drops signatures (after the `--` line), and
collapses whitespace.

## Testing Strategy

| Package | Approach |
|---|---|
| `policy` | Table-driven, no network. The most important tests in the project. |
| `extract` | Golden files from real messages: dirty HTML, quoted replies, signatures. |
| `laya` | `httptest.Server` serving recorded responses; then a manual run against the real server. |
| `gmail` | Defined behind an interface, with a fake implementation. |
| `audit` | Round-trip and rollback-replay tests. |
| End-to-end | `run --dry-run` against a fake Laya server. |

## Milestones

Each milestone ends in something runnable.

1. **Repo + OAuth + read.** `git init`, `.gitignore`, `setup`, list the inbox and
   print headers. No Laya, no actions. This is where the credentials get solved.
2. **`extract`.** `Message` → `State`, with golden-file tests.
3. **`internal/laya`.** Client against `httptest.Server` with recorded responses,
   then against the real server.
4. **`policy` + dry run.** `run --dry-run --limit 150`. **Thresholds are
   calibrated here.**
5. **Actions + audit.** `act`, `audit.jsonl`, `rollback`.
6. **Robustness.** Circuit breaker, pause, resume, quota handling.

Milestone 4 is the real gate: a 150-message dry run shows whether the system is
worth trusting before it changes anything.

## Open Item: Calibration

`min_confidence_junk` (0.90) and `min_confidence_topic` (0.70) are starting
values, not measured ones. The model ships over-confident, so these must be set
from the confidence distribution observed in the milestone-4 dry run. The audit
log records every confidence value to make that possible.

## References

- Laya model card — https://huggingface.co/convaiinnovations/laya
- Gmail API usage limits — https://developers.google.com/workspace/gmail/api/reference/quota
- Google OAuth 2.0 — https://developers.google.com/identity/protocols/oauth2
- Laya issue #156 (`noul` label-following) and #185 (`action.act_probability`)
