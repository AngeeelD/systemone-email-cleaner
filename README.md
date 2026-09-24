# systemone-email-cleaner

A local Go CLI that **groups your Gmail inbox automatically**. It reads each message's
headers and a short body preview, asks a [System One](#model-backends) decision model to
classify it, applies a `cleaner/*` label, and moves genuine junk to Trash.

Nothing is ever permanently deleted, every action is written to an append-only audit log,
and any run can be rolled back.

---

## Quick start

1. **Prerequisites** — Go 1.27+, a Google Cloud OAuth *desktop* client, and a System One
   endpoint (see [Model backends](#model-backends)).
2. **Get credentials** — in Google Cloud, enable the Gmail API and create an OAuth client
   of type *Desktop app*. Download the JSON as `client_secret.json` in this directory.
3. **Configure** — copy the example and point it at your model endpoint:

   ```bash
   cp config.example.yaml config.yaml
   ```

4. **Authorize and create labels** (once, then again whenever the OAuth app's 7-day
   Testing-mode token expires):

   ```bash
   go run ./cmd/emailcleaner setup
   ```

5. **Preview, then act:**

   ```bash
   export LAYA_API_KEY=...                                        # the env var named in config
   go run ./cmd/emailcleaner run --dry-run --limit 50 --workers 2  # prints the plan, writes nothing
   go run ./cmd/emailcleaner run --limit 500 --workers 2           # applies it
   ```

6. **Verify** — `cleaner/*` labels appear in Gmail, junk is in Trash, and
   `audit/` has one JSONL record per message.

## Commands

| Command | What it does |
|---|---|
| `status` | Report token health and the configured label set. Read-only. |
| `setup` | Authorize with Gmail and create any missing labels. Idempotent. |
| `list` | Print the headers of unprocessed inbox messages. |
| `run` | Classify and act. `--dry-run` prints the plan and writes nothing. |
| `rollback` | Undo a run (default: the latest). |

`run` flags: `--dry-run`, `--limit N`, `--workers N`, `--reprocess unclassified`,
`--min-confidence-junk F`, `--min-confidence-topic F`.

## Labels

| Label | Meaning |
|---|---|
| `cleaner/people` | A real person writing to you |
| `cleaner/banking` | A bank or fintech transaction |
| `cleaner/accounts` | Purchase, order, receipt, or delivery |
| `cleaner/security` | Sign-in or account-security event |
| `cleaner/unclassified` | Below threshold — manual review, never a guess |
| *(Trash)* | Unwanted bulk / marketing |

Only junk loses `INBOX`. Every other message stays in the inbox and simply gains a label.

## How it works

```
in:inbox -label:cleaner/*  →  Get (format=full)  →  extract (headers + body preview)
        →  System One model (5 choice questions)  →  policy (pure Go)  →  label / trash
```

- `internal/policy` is **pure** — no network, no I/O — because it is the only place that
  can lose a message. It is the most heavily tested package.
- A circuit breaker pauses after 5 consecutive model failures and resumes from the same
  point. One unreadable or unclassifiable message is skipped, never fatal.

## Model backends

The classifier speaks the **System One** protocol: `POST /v1/systemone` with
`{"state", "model", "questions"}`, returning typed `answers` with `choice`,
`probabilities`, and `confidence`. Any compatible endpoint works.

| Backend | Notes |
|---|---|
| [Laya](https://huggingface.co/convaiinnovations/laya) | Open source, local. Very fast (~10 ms), but noisy on real mail — expect more `unclassified`. |
| Kev | Open source, local. Cleaner separation, ~200 ms. |
| [Jev](https://typesafe.ai/) (TypeSafe) | Hosted. Use `model: "jev-latest"`. |

Set `laya.endpoint` to the server and `laya.api_key_env` to the environment variable
holding its key. The client sends a language hint (`english` / `multilingual`) in the
`model` field; a hosted backend that validates model names may need its own value.

## Configuration

See `config.example.yaml`. `config.yaml` is gitignored — it holds your endpoint and
thresholds, which are machine-specific.

## Safety

- **Scope:** `https://www.googleapis.com/auth/gmail.modify` only. No `mail.google.com`,
  no `messages.delete`.
- **"Delete" always means Trash** — recoverable for 30 days.
- `client_secret.json` and `token.json` are gitignored and written `0600`.
- Every run appends to `audit/`, which is gitignored because it contains subjects and
  sender addresses.

## Development

```bash
go test -race ./...
go vet ./...
```

## License

[MIT](LICENSE) — free to use, modify, and distribute.
