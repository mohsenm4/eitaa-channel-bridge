# Eitaa Channel Bridge

A Go bot that watches a public **Eitaa** channel and automatically republishes each message as a post on a **WordPress** site. It routes messages by hashtag, mirrors edits and deletions, uploads photos, and survives restarts via a local JSON ledger.

Originally built for the *Meraj Cultural & Religious Institute* ([@Merajyan](https://eitaa.com/Merajyan)) → [fatemyoon.ir](https://fatemyoon.ir), but every value (channel, categories, WP credentials) lives in `.env` — you can point it at any public Eitaa channel and any self-hosted WordPress install.

---

## Table of contents

1. [What it does](#1-what-it-does)
2. [Quick start](#2-quick-start)
3. [Setting up WordPress](#3-setting-up-wordpress)
4. [Post format (emoji markers)](#4-post-format-emoji-markers)
5. [Configuration reference](#5-configuration-reference)
6. [How hot vs. cold polling works](#6-how-hot-vs-cold-polling-works)
7. [Homepage auto-update](#7-homepage-auto-update)
8. [Health monitoring](#8-health-monitoring)
9. [Commands](#9-commands)
10. [Deployment](#10-deployment)
11. [Project layout](#11-project-layout)
12. [Known limitations](#12-known-limitations)

---

## 1) What it does

```text
┌───────────────┐      ┌────────────┐      ┌──────────────┐
│ Eitaa channel │  →   │ Bridge bot │  →   │ WordPress    │
│  (source)     │      │ (this app) │      │  (REST API)  │
└───────────────┘      └────────────┘      └──────────────┘
```

**Per-message pipeline:**

1. Polls the channel page on a schedule (see [hot vs. cold](#6-how-hot-vs-cold-polling-works)).
2. For each new message:
   - Extracts hashtags, title, date, and body via emoji markers (see [Post format](#4-post-format-emoji-markers)).
   - Routes to a WP category based on hashtag. Drops messages with no match (or queues them for manual review).
   - Uploads photos as WP media — first photo becomes the featured image, the rest go into a gallery.
   - Publishes via `/wp-json/wp/v2/posts` — or via the bundled [`eitaa-bridge-helper`](wp-plugin/eitaa-bridge-helper/) plugin when installed (preserves WPBakery/theme layout meta).
3. If the author **edits** a message → pushes an update to the WP post.
4. If the author **deletes** a message → sends the WP post to trash.
5. After each publish, optionally rewrites the homepage poster slideshow links to point at the newest post per category.
6. Every action is recorded in `data/seen.json` so no message is ever sent twice.

**Reliability features:**
- Atomic JSON state writes (temp file + rename — crash-safe).
- Dead-man's-switch health check (e.g. healthchecks.io) with automatic DOWN signal after consecutive failures.
- Photo deduplication by content hash — same photo across multiple messages reuses the same WP media entry.
- Duplicate-message detection — skips Eitaa's occasional double-send glitch.

---

## 2) Quick start

Requires **Go 1.22+** and a WordPress site you can log into.

```sh
git clone https://github.com/mohsenm4/eitaa-channel-bridge
cd eitaa-channel-bridge
cp .env.example .env
# edit .env — at minimum set:
#   EITAA_CHANNEL, CATEGORIES, WP_URL, WP_USER, WP_APP_PASSWORD

go run ./cmd/bridge dump   # one-shot preview (no publishing)
go run ./cmd/bridge run    # continuous mode
```

Or with Docker:

```sh
docker compose up -d
```

---

## 3) Setting up WordPress

### 3.1 Generate an Application Password

1. Log in to WP admin as a user who can publish posts.
2. **Users → Profile → Application Passwords**.
3. Enter a name (e.g. `eitaa-bridge`) and click **Add New Application Password**.
4. Copy the generated value (spaces included) into `.env`:

```env
WP_URL=https://your-site.com
WP_USER=your-wp-username
WP_APP_PASSWORD=abcd efgh ijkl mnop qrst uvwx
```

### 3.2 Map hashtags to WP categories

In `.env`, set `CATEGORIES` as a comma-separated list of `hashtag|slug|label` triples:

```env
CATEGORIES="حمایت_خدمت|hemayat-khedmat|حمایت خدمت,قرض_الحسنه|qarz-al-hasaneh|قرض الحسنه"
```

- **hashtag** — the Eitaa hashtag (without `#`) used by content editors.
- **slug** — the WP category slug (must already exist in WP).
- **label** — the human-readable WP category name.

A message is published **only** if one of its hashtags matches an entry here. Unmatched messages are silently dropped (or routed to `INBOX_HASHTAGS`).

### 3.3 Optional: install the helper plugin

The bridge can publish via the standard WP REST API, but a stock REST post loses theme and page-builder layout meta. The bundled [`wp-plugin/eitaa-bridge-helper/`](wp-plugin/eitaa-bridge-helper/) plugin clones an existing post (carrying over all meta) and then overlays the new content.

1. Zip `wp-plugin/eitaa-bridge-helper/`.
2. WP admin → **Plugins → Add New → Upload Plugin** → activate.
3. The bridge auto-detects it on first publish — no extra config needed.

The plugin also exposes the homepage poster-update endpoint used by the [Homepage auto-update](#7-homepage-auto-update) feature (requires v1.5+).

### 3.4 Category templates

For categories that have a fixed visual layout (WPBakery shortcodes, custom fields, etc.), place a template file at:

```
internal/publisher/templates/{category-slug}.tmpl
```

The bridge renders the template with these variables and uses the result as the post content:

| Variable | Value |
| --- | --- |
| `{{TITLE}}` | Extracted from `📌` marker |
| `{{EVENT_DATE}}` | Extracted from `📅` marker |
| `{{BODY}}` | Extracted from `📝` marker |

If no template file exists for a category, the bridge falls back to cloning the most recent post in that category, then to a plain HTML wrap if no previous post exists.

---

## 4) Post format (emoji markers)

Content editors write Eitaa messages with special emoji markers. The bridge reads these to extract structured fields:

| Marker | Field extracted |
| --- | --- |
| `📌` | Post title |
| `📅` | Event date |
| `📝` | Post body |
| `🟩` | Subtitle / section header |

**Example Eitaa message:**

```
#حمایت_خدمت

📌 اجرای برنامه یاری‌رسان
📅 ۱۴۰۳/۰۳/۱۵
📝 در این برنامه تعداد ۵۰ خانواده تحت پوشش قرار گرفتند...
```

If no `📌` marker is present, the bridge uses the first non-empty line as the title and logs the message to `data/warnings.log` for review.

---

## 5) Configuration reference

All config is environment variables. Copy `.env.example` to `.env` and fill in values. Real env vars always override `.env`.

### Source

| Variable | Default | Purpose |
| --- | --- | --- |
| `EITAA_CHANNEL` | *(required)* | Channel username on eitaa.com (no leading `@`) |
| `POLL_COLD` | `6h` | Slow poll interval when nothing recent is being watched |
| `POLL_HOT` | `10s` | Fast poll interval while a tracked message is inside `EDIT_WINDOW` |
| `EDIT_WINDOW` | `1h` | How long after publishing a message to keep polling fast. Keep larger than `POLL_COLD` |
| `SYNC_WINDOW` | `720h` | How far back (by message date) published posts stay watched for edits/deletes; the bridge pages through `?before=` to cover it. Go duration — no `d` unit, so `1440h` = 60 days |
| `BACKFILL` | `0` | On the first run only, walk back this many older messages via pagination |

### Publishing

| Variable | Default | Purpose |
| --- | --- | --- |
| `CATEGORIES` | *(required)* | Comma-separated `hashtag\|slug\|label` triples |
| `DEFAULT_CATEGORY` | *(empty)* | `slug\|label` fallback for messages with no matching hashtag |
| `SKIP_HASHTAGS` | *(empty)* | Hashtags whose messages are silently ignored |
| `INBOX_HASHTAGS` | *(empty)* | Hashtags routed to `data/inbox.log` for manual review instead of auto-publishing |
| `DEDUPE_WINDOW` | `10s` | Skip a new message if it has the same text as a recently published one (Eitaa double-send guard) |

### WordPress target

| Variable | Default | Purpose |
| --- | --- | --- |
| `WP_URL` | *(required)* | WordPress site URL |
| `WP_USER` | *(required)* | WP username with publish rights |
| `WP_APP_PASSWORD` | *(required)* | WP application password |
| `WP_POST_TYPE` | `post` | Custom post type slug |
| `WP_STATUS` | `draft` | `draft` / `publish` / `private` / `pending` |

### Homepage auto-update

No configuration needed — see [Homepage auto-update](#7-homepage-auto-update) for what the bridge does automatically.

### Health monitoring

| Variable | Default | Purpose |
| --- | --- | --- |
| `HEALTHCHECK_URL` | *(empty)* | Dead-man's-switch URL (e.g. healthchecks.io). Empty = disabled |
| `HEALTHCHECK_INTERVAL` | `12m` | How often to ping the healthcheck URL |
| `HEALTHCHECK_FAIL_THRESHOLD` | `3` | Consecutive publish failures before flipping the check to DOWN |

### Storage & logging

| Variable | Default | Purpose |
| --- | --- | --- |
| `SEEN_FILE` | `data/seen.json` | Per-channel ledger of processed messages |
| `ARCHIVE_FILE` | `data/messages.jsonl` | Raw archive of every message seen |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

---

## 6) How hot vs. cold polling works

The bridge uses two speeds to balance freshness and bandwidth:

- **Cold** (`POLL_COLD`, default `6h`) — steady-state rate. Nothing recent is being watched for edits.
- **Hot** (`POLL_HOT`, default `10s`) — kicks in after publishing a message younger than `EDIT_WINDOW`, so edits and deletions are caught before the author moves on.

After every tick the bridge checks: is the freshest tracked message newer than `EDIT_WINDOW` ago? If yes → hot. If no → cold.

**Important:** `EDIT_WINDOW` must be larger than `POLL_COLD`. If it is not, a fresh message discovered on a cold poll is already older than the window and hot mode never activates. The bridge prints a warning at startup if this constraint is violated. A safe rule of thumb: `EDIT_WINDOW ≥ 2 × POLL_COLD`.

**Which posts are watched for edits/deletes:** every published post dated within `SYNC_WINDOW`. Eitaa's first page can be very short (a handful of messages), so on every tick the bridge pages back through `?before=` until the fetched range reaches the oldest watched post (capped at 10 pages per tick). A post deleted after it scrolled off the first page is therefore still trashed on WordPress. Posts older than the window are left alone.

---

## 7) Homepage auto-update

After each publish the bridge can automatically rewrite the poster slideshow on the static front page so every poster card always links to the most recent post in its category.

**How it works:**

1. On startup, the bridge asks WP which page is the static front page (Settings → Reading).
2. It walks the `av_slide` elements on that page and maps each slide to a category by matching the poster image filename against known category slugs.
3. After each new post is published, it rewrites the matching slide's link via the helper plugin endpoint.

**Requirements:**
- `eitaa-bridge-helper` plugin v1.5+ must be installed and active.
- Poster images should be named after the category slug (e.g. `hemayat-khedmat-poster.png`) so auto-discovery works.

---

## 8) Health monitoring

Set `HEALTHCHECK_URL` to a [healthchecks.io](https://healthchecks.io) (or compatible) URL. The bridge pings it on startup and every `HEALTHCHECK_INTERVAL` while running. If pings stop, the external service alerts you.

When `HEALTHCHECK_FAIL_THRESHOLD` consecutive message publishes fail, the bridge pings `{HEALTHCHECK_URL}/fail` instead of the plain URL, immediately flipping the check to **DOWN** — even though the process is still alive and pinging.

Keep `HEALTHCHECK_INTERVAL` a few minutes below the **Period** configured at the monitoring service to avoid false alarms from a single delayed ping.

---

## 9) Commands

All commands read `.env` by default. Override with `--env PATH`.

### `bridge dump` — one-shot preview

```sh
go run ./cmd/bridge dump
```

Fetches the channel once, classifies each message, and writes to `data/last_dump.json` and `data/raw.html`. **Does not publish anything.** Use this to verify the parser and preview routing before going live.

### `bridge run` — continuous mode

```sh
go run ./cmd/bridge run
```

The main loop: polls, publishes new messages, syncs edits and deletes to WP, archives raw payloads, and persists state after every action. Safe to stop and restart at any time.

### `bridge seed` — initialize without publishing

```sh
go run ./cmd/bridge seed
```

Marks all currently visible messages as "seen" without publishing them. Run this once before switching to `run` on a channel that already has content, so the bridge does not flood WP with old posts.

---

## 10) Deployment

### Build

```sh
make build    # compiles to bin/bridge
make test     # unit tests
make check    # fmt + vet + test
```

### Docker

```sh
docker compose up -d       # build and start
make logs                  # stream container logs
make docker-dump           # one-shot dump inside the container
make shell                 # exec a shell inside the container
```

### Systemd (production)

```sh
sudo cp deploy/eitaa-bridge.service /etc/systemd/system/
sudo systemctl daemon-reload
make svc-enable    # enable and start on boot
make svc-logs      # tail journalctl output
make svc-restart   # rebuild and restart
```

---

## 11) Project layout

```text
eitaa-channel-bridge/
├── .env.example                       ← copy to .env and fill in values
├── Dockerfile / docker-compose.yml
├── Makefile
├── cmd/bridge/
│   ├── main.go                        ← CLI entry point (dump | run | seed)
│   ├── run.go                         ← polling loop, edit/delete sync
│   ├── dump.go                        ← one-shot fetch preview
│   └── sidelog.go                     ← inbox.log / warnings.log writers
├── internal/
│   ├── config/                        ← env-var loader and validation
│   ├── eitaa/                         ← fetches and HTML-parses the channel page
│   ├── router/                        ← hashtag→category routing, emoji marker extraction
│   ├── publisher/                     ← WP REST client, post rendering, photo upload
│   │   └── templates/                 ← per-category content templates ({slug}.tmpl)
│   ├── state/                         ← seen-IDs ledger (id → post, fingerprint, ts, deleted)
│   ├── jsonio/                        ← JSON / JSONL read-write helpers
│   └── utils/                         ← structured logger, text display helpers
├── wp-plugin/eitaa-bridge-helper/     ← optional WP plugin for layout-preserving post clone
└── data/                              ← runtime files (gitignored)
    ├── seen.json                      ← per-message state (eitaa-id → {post_id, fp, ts, deleted})
    ├── messages.jsonl                 ← raw archive of every message seen
    ├── inbox.log                      ← INBOX_HASHTAGS messages queued for manual review
    ├── warnings.log                   ← messages published with missing/incomplete markers
    ├── last_dump.json                 ← classified output from the last `dump` run
    └── raw.html                       ← raw HTML from the last fetch
```

---

## 12) Known limitations

- **Unofficial parser.** Eitaa has no documented public API. The bridge parses the public channel HTML page. If Eitaa changes its markup the parser will need updating.
- **Public channels only.** Private groups and channels are not accessible.
- **Visible page only.** Edit and delete detection only covers the ~20 most recent messages that Eitaa renders on the channel page. Older messages that scroll off cannot be re-checked.
- **TLS.** The bridge currently skips TLS certificate verification for the target WP site (workaround for an expired cert on fatemyoon.ir). A warning is logged at startup.

---

## License

Internal use — Meraj Cultural & Religious Institute.
