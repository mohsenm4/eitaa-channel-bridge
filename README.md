# Eitaa Channel Bridge

A small Go bot that watches a public **Eitaa** channel and republishes each new message as a post on a **WordPress** site. It routes messages by hashtag into existing WP categories, mirrors author edits and deletions, and survives restarts via a local JSON ledger.

Originally built for the *Meraj Cultural & Religious Institute* ([@Merajyan](https://eitaa.com/Merajyan)) → [fatemyoon.ir](https://fatemyoon.ir), but every value (channel, target site, category mapping) lives in `.env` — you can point it at any public Eitaa channel and any self-hosted WP install.

---

## 1) What it does

```text
┌───────────────┐      ┌────────────┐      ┌──────────────┐
│ Eitaa channel │  →   │ Bridge bot │  →   │ WordPress    │
│  (source)     │      │ (this app) │      │  (REST API)  │
└───────────────┘      └────────────┘      └──────────────┘
```

1. Polls the source channel — **fast** (`POLL_HOT`) while a recent message is still inside `EDIT_WINDOW`, **slow** (`POLL_COLD`) otherwise.
2. For every new message:
   - reads its hashtag,
   - decides which WP category to use (or skip / queue),
   - uploads any photos as media,
   - publishes a post via `/wp-json/wp/v2/posts` — or via the bundled [`eitaa-bridge-helper`](wp-plugin/eitaa-bridge-helper/) plugin when installed (preserves WPBakery/theme layout meta).
3. If the author **edits** a message on Eitaa → pushes an update to the WP post.
4. If the author **deletes** a message on Eitaa → sends the WP post to trash.
5. Every action is recorded in `data/seen.json` so no message is sent twice — even after restarts.

---

## 2) Quick start

Requires Go 1.22+ and a WordPress site you can log into.

```sh
git clone https://github.com/mohsenm4/eitaa-channel-bridge
cd eitaa-channel-bridge
cp .env.example .env
# edit .env — at minimum: EITAA_CHANNEL, CATEGORIES, WP_URL, WP_USER, WP_APP_PASSWORD
go run ./cmd/bridge dump     # one-shot fetch + classification preview (no publishing)
go run ./cmd/bridge run      # continuous mode
```

Or with Docker:

```sh
docker compose up -d
```

---

## 3) Setting up WordPress

You need three things from the WP side: an app password, a category map, and (optionally) the helper plugin for full-fidelity posts.

### 3.1 Generate a WordPress Application Password

1. Log in to WP admin as a user who can publish posts.
2. **Users → Profile** → scroll to **Application Passwords**.
3. Enter a name (e.g. `eitaa-bridge`) and click **Add New Application Password**.
4. Copy the generated value (with spaces) into `WP_APP_PASSWORD` in `.env`.

```env
WP_URL=https://your-site.com
WP_USER=your-wp-username
WP_APP_PASSWORD=abcd efgh ijkl mnop qrst uvwx
```

### 3.2 Map hashtags → WP categories

In `.env`, set `CATEGORIES` as a comma-separated list of `hashtag|slug|label` triples:

```env
CATEGORIES="news|news|News,events|events|Events"
```

- **hashtag** — the Persian/English hashtag used in Eitaa posts (no `#`).
- **slug** — the WP category slug (must already exist in WP, or be created via the helper plugin).
- **label** — the human-readable category name shown in WP.

A message is published **only** if one of its hashtags matches an entry here. Anything else is dropped (or routed to `INBOX_HASHTAGS` for manual review).

### 3.3 Optional: install the helper plugin

The bot can publish via the standard REST API, but a stock REST post can lose theme/page-builder layout meta. The bundled plugin [`wp-plugin/eitaa-bridge-helper/`](wp-plugin/eitaa-bridge-helper/) clones an existing post (preserving meta) and then overlays the new content:

1. Zip the `wp-plugin/eitaa-bridge-helper/` directory.
2. WP admin → **Plugins → Add New → Upload Plugin** → activate.
3. The bot auto-detects it on first publish — no extra config needed.

---

## 4) Configuration reference

All config is environment variables (with a `.env` file as fallback). Real env vars always win.

### Source

| Variable | Default | Purpose |
| --- | --- | --- |
| `EITAA_CHANNEL` | *(required)* | Channel username on eitaa.com (no leading `@`) |
| `POLL_COLD` | `6h` | Slow poll interval used when nothing recent is being watched |
| `POLL_HOT` | `10s` | Fast poll interval used while a tracked message is inside `EDIT_WINDOW` |
| `EDIT_WINDOW` | `1h` | After publishing a message, how long to keep polling fast for edits/deletes. **Keep this larger than `POLL_COLD`** or hot mode rarely activates |
| `BACKFILL` | `0` | On the FIRST run only, walk back this many older messages |
| `HEALTHCHECK_URL` | *(empty)* | If set, the bot pings this URL on start + every `HEALTHCHECK_INTERVAL` (use [healthchecks.io](https://healthchecks.io) as a dead-man's-switch) |
| `HEALTHCHECK_INTERVAL` | `12m` | How often to ping `HEALTHCHECK_URL` |

### Publishing

| Variable | Default | Purpose |
| --- | --- | --- |
| `CATEGORIES` | *(required)* | Comma-separated `hashtag\|slug\|label` triples |
| `DEFAULT_CATEGORY` | *(empty)* | `slug\|label` for messages with no matching hashtag (omit to drop them) |
| `SKIP_HASHTAGS` | *(empty)* | Hashtags whose messages should be ignored entirely |
| `INBOX_HASHTAGS` | *(empty)* | Hashtags whose messages go to `data/inbox.log` for manual review instead of auto-publishing |
| `DEDUPE_WINDOW` | `10s` | Treat a same-text message within this window of an existing post as Eitaa's double-send glitch and skip it |

### WordPress target

| Variable | Default | Purpose |
| --- | --- | --- |
| `WP_URL` | *(required)* | WordPress site URL (e.g. `https://example.com`) |
| `WP_USER` | *(required)* | WP username with publish rights |
| `WP_APP_PASSWORD` | *(required)* | WP application password (see 3.1) |
| `WP_POST_TYPE` | `post` | Post type slug |
| `WP_STATUS` | `draft` | `draft` / `publish` / `private` / `pending` |

### Storage & logging

| Variable | Default | Purpose |
| --- | --- | --- |
| `SEEN_FILE` | `data/seen.json` | Per-channel ledger of processed messages |
| `ARCHIVE_FILE` | `data/messages.jsonl` | Raw archive of every message seen |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

---

## 5) Understanding hot vs. cold mode

The bot uses two polling speeds:

- **Cold** (`POLL_COLD`, default 6h): the steady-state rate. Nothing recent is being watched for edits.
- **Hot** (`POLL_HOT`, default 10s): kicks in as soon as a message younger than `EDIT_WINDOW` is tracked.

The decision is made after every tick: if the freshest tracked message has an Eitaa timestamp newer than `EDIT_WINDOW` ago → hot, otherwise → cold.

**Important:** if `EDIT_WINDOW < POLL_COLD`, hot mode will almost never activate — by the time a cold poll discovers a fresh message, it's already older than the window. A good rule of thumb is `EDIT_WINDOW ≥ 2 × POLL_COLD`. The bot prints a warning at startup if this is violated.

---

## 6) Commands

Both commands read `.env` by default; override with `--env PATH`.

### `bridge dump` — one-shot inspection

```sh
go run ./cmd/bridge dump
```

Fetches the channel once and writes raw HTML + classified JSON under `data/`. **Does NOT publish.** Use this to verify the parser and preview which posts WOULD be published.

### `bridge run` — continuous mode

```sh
go run ./cmd/bridge run
```

Polls, publishes new messages, syncs edits/deletes onto WP, archives raw payloads, and persists state after every terminal action. Safe to stop and restart.

---

## 7) Project layout

```text
eitaa-channel-bridge/
├── .env.example                ← copy to .env
├── Dockerfile / docker-compose.yml
├── cmd/bridge/
│   ├── main.go                 ← CLI entry point (dump, run)
│   ├── run.go                  ← polling loop + edit/delete sync
│   ├── dump.go                 ← one-shot fetch preview
│   └── sidelog.go              ← inbox.log / warnings.log helpers
├── internal/
│   ├── config/                 ← env-var loader + validation
│   ├── eitaa/                  ← fetches & parses channel HTML
│   ├── router/                 ← hashtag → category routing, title/date extraction
│   ├── publisher/              ← WordPress REST client + category templates
│   ├── state/                  ← seen-IDs + fingerprint + WP post-ID store
│   ├── jsonio/                 ← JSON/JSONL read/write helpers
│   └── utils/                  ← logger, display helpers
├── wp-plugin/eitaa-bridge-helper/   ← optional WP plugin for full-meta clone
└── data/                       ← runtime files (gitignored)
    ├── seen.json               ← id → {post, fp, ts, deleted}
    ├── messages.jsonl          ← raw archive of every message seen
    ├── inbox.log               ← INBOX_HASHTAGS routed here
    ├── warnings.log            ← per-message format issues
    ├── last_dump.json          ← pretty JSON from the last dump
    └── raw.html                ← raw HTML from the last fetch
```

---

## 8) Known limitations

- **Unofficial.** Eitaa has no documented API; the parser depends on public-channel HTML and will need updates if that markup changes.
- **Public channels only.** Private groups and channels are not accessible.
- **Visible page only.** Edits and deletions are detected by comparing the seen-store against the most recent ~20 messages Eitaa renders. Messages that fall off the page can't be re-checked.

---

## License

Internal use — Meraj Cultural & Religious Institute.
