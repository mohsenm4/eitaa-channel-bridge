# Eitaa Channel Bridge

> A small bot that reads every new message published in a public Eitaa channel and republishes each one as a post on a WordPress site, classified by hashtag into the site's existing categories.
>
> Built for **Meraj Cultural & Religious Institute** ([@Merajyan](https://eitaa.com/Merajyan)) → [fatemyoon.ir](https://fatemyoon.ir), but the channel and the destination WP site are both configured via `.env`, so it works for any public Eitaa channel + WP install.

---

## 1) What it does

```
┌───────────────┐      ┌──────────────┐      ┌──────────────┐
│ Eitaa channel │  →   │  Bridge bot   │  →   │ WordPress    │
│ (source)      │      │ (this project)│      │ (REST API)   │
└───────────────┘      └──────────────┘      └──────────────┘
```

1. The bot polls the source channel (hot mode while recent messages are still inside the edit-watch window, cold mode otherwise).
2. It identifies new messages, classifies each by hashtag, uploads photos to the WP media library, and creates a post via `/wp-json/wp/v2/posts` (or via the bundled `eitaa-bridge-helper` plugin when installed — preserves WPBakery / theme layout meta).
3. When the author edits a message on Eitaa, the bot detects the fingerprint change and pushes a `POST /wp-json/wp/v2/posts/{id}` update.
4. When the author deletes a message on Eitaa, the bot detects its disappearance from the visible page and sends the WP post to trash.
5. Every published / skipped / inbox-routed ID is recorded in `data/seen.json` so the same message is never sent twice — even across restarts.

---

## 2) Project layout

```
eitaa-channel-bridge/
├── README.md
├── .env.example                ← sample env file (copy to .env)
├── go.mod / go.sum
├── Dockerfile / docker-compose.yml
├── cmd/
│   └── bridge/
│       ├── main.go             ← CLI entry point (dump, run)
│       ├── run.go              ← polling loop + edit/delete sync
│       ├── dump.go             ← one-shot fetch + classification preview
│       └── sidelog.go          ← inbox.log / warnings.log helpers
├── internal/
│   ├── config/                 ← env-var loader + validation
│   ├── eitaa/                  ← fetches & parses the channel HTML
│   ├── router/                 ← hashtag → category routing + title/date extraction
│   ├── publisher/              ← WordPress REST client + category templates
│   ├── state/                  ← seen-IDs + fingerprint + WP post-ID store
│   ├── jsonio/                 ← JSON/JSONL read/write helpers
│   └── utils/                  ← misc (logger, display helpers)
├── wp-plugin/
│   └── eitaa-bridge-helper/    ← optional WP plugin for full-meta clone
└── data/                       ← runtime files (gitignored)
    ├── seen.json               ← per-channel ledger: id → {post, fp, ts, deleted}
    ├── messages.jsonl          ← raw archive of every message seen
    ├── inbox.log               ← hashtags routed for manual review
    ├── warnings.log            ← per-message format issues
    ├── last_dump.json          ← pretty JSON from the last `bridge dump`
    └── raw.html                ← raw HTML from the last fetch
```

---

## 3) Configuration

All config is environment variables, optionally seeded by a local `.env`. Copy the example:

```sh
cp .env.example .env
```

Required variables:

| Variable | Purpose |
| --- | --- |
| `EITAA_BRIDGE_SOURCE_CHANNEL` | channel username, no leading `@` |
| `EITAA_BRIDGE_PUBLISHING_CATEGORIES` | comma-separated `hashtag\|slug\|label` triples |
| `EITAA_BRIDGE_TARGET_WORDPRESS_URL` | site URL |
| `EITAA_BRIDGE_TARGET_WORDPRESS_USERNAME` | WP user with publish rights |
| `EITAA_BRIDGE_TARGET_WORDPRESS_APP_PASSWORD` | application password |

See [.env.example](.env.example) for the full schema with defaults and inline docs.

---

## 4) Commands

Build:

```sh
go build -o bin/bridge ./cmd/bridge
```

Both commands read `.env` by default (override with `--env PATH`).

### `bridge dump` — one-shot inspection

Fetches the channel once and writes raw HTML + the classified JSON to `data/`. Does NOT publish. Use this to verify the parser and preview which posts WOULD be published.

```sh
go run ./cmd/bridge dump
```

### `bridge run` — continuous mode

Polls the channel, publishes new messages, syncs edits/deletes onto WP, archives raw payloads, and persists state after every terminal action. Safe to stop and restart — already-processed messages are skipped on the next run.

```sh
go run ./cmd/bridge run
```

---

## 5) Parsed message format

Every parsed `Message` has these fields:

| Field | Type | Notes |
| --- | --- | --- |
| `id` | int | the numeric message ID inside the channel |
| `channel` | string | channel username, without `@` |
| `link` | string | canonical URL on eitaa.com |
| `author` | string | post author / owner name |
| `forwarded_from` | string | only set when the message was forwarded |
| `date` | RFC3339 timestamp | from `<time datetime=…>` |
| `views` | int | view counter |
| `text` | string | plain text with newlines preserved |
| `text_html` | string | inner HTML of the message bubble |
| `photos` | string[] | URLs of attached photos (empty for text-only posts) |
| `reply_to_id` | int | the source-channel ID this post replies to (0 if not a reply) |

---

## 6) Known limitations

- **Unofficial.** Eitaa has no documented API; the parser depends on the public-channel HTML and will need updates if that markup changes.
- **Public channels only.** Private groups and channels are not accessible.
- **Visible page only.** Edits and deletions are detected by comparing the seen-store against the most recent ~20 messages Eitaa renders. Messages that fall off the page can't be re-checked.
- **Rate limits.** Cold-mode default is one fetch every 6 hours; hot mode (10s) only kicks in while a tracked message is still inside `EDIT_WATCH_WINDOW`.

---

## License

Internal use — Meraj Cultural & Religious Institute.
