# Eitaa Channel Bridge

> A small bot that reads every new message published in a public Eitaa channel and republishes it on a target site, using a defined format.
>
> Built for the channel of **Meraj Cultural & Religious Institute** ([@Merajyan](https://eitaa.com/Merajyan)), but the channel and the target site are both configured via `config.yaml`, so it works for any public Eitaa channel.

---

## 1) What it does

```
┌───────────────┐      ┌──────────────┐      ┌──────────────┐
│ Eitaa channel │  →   │  Bridge bot   │  →   │ Target site  │
│ (source)      │      │ (this project)│      │ (configured)  │
└───────────────┘      └──────────────┘      └──────────────┘
```

1. Every few minutes the bot checks the source channel.
2. It identifies new messages (those it has not seen before).
3. It publishes each new message via the configured target.
4. It records every published ID in `data/seen.json` so nothing is sent twice.

---

## 2) Project layout

```
eitaa-channel-bridge/
├── README.md
├── config.yaml.example          ← sample config (copy to config.yaml)
├── go.mod / go.sum
├── cmd/
│   └── bridge/
│       └── main.go              ← CLI entry point (dump, run)
├── internal/
│   ├── config/
│   │   └── config.go            ← YAML loader + validation
│   ├── eitaa/
│   │   └── reader.go            ← fetches & parses the channel HTML
│   ├── publisher/
│   │   ├── publisher.go         ← Publisher interface + factory
│   │   └── file.go              ← writes messages to a JSON Lines file
│   └── state/
│       └── state.go             ← tracks which message IDs were published
└── data/                        ← runtime files (gitignored)
    ├── seen.json                ← processed message IDs
    ├── messages.jsonl           ← raw archive of every message seen
    ├── published.jsonl          ← messages delivered to the target
    ├── last_dump.json           ← pretty JSON from the last `bridge dump`
    └── raw.html                 ← raw HTML from the last fetch
```

Three things shape the bridge:

- **Source** (`internal/eitaa`) — fetches `https://eitaa.com/<channel>` and parses each `.etme_widget_message` block into a `Message` struct.
- **Publisher** (`internal/publisher`) — an interface with one implementation today (`file`). When the destination site is decided, a new publisher is added next to `file.go`.
- **State** (`internal/state`) — a JSON-backed set of seen message IDs, keyed by channel.

---

## 3) Configuration

Copy the example and edit:

```sh
cp config.yaml.example config.yaml
```

`config.yaml` is gitignored. The schema:

```yaml
source:
  channel: Merajyan           # channel username, without @
  poll_interval: 5m           # 30s, 5m, 1h, …

target:
  type: file                  # only "file" is implemented today
  file:
    path: data/published.jsonl

storage:
  seen_file: data/seen.json
  archive_file: data/messages.jsonl
```

The loader applies defaults for unset fields and rejects the config with a clear error if anything required is missing or invalid (e.g. `source.channel must not include the leading @`).

---

## 4) Commands

Build:

```sh
go build -o bin/bridge ./cmd/bridge
```

Both commands read `config.yaml` by default (override with `--config PATH`).

### `bridge dump` — one-shot inspection

Fetches the channel once and writes raw HTML, pretty JSON, and the archive to disk. Does NOT publish. Use this to verify the parser and inspect the structure of messages.

```sh
go run ./cmd/bridge dump
```

Sample output:

```
channel:  @Merajyan
messages: 6
raw html: data/raw.html
pretty:   data/last_dump.json
archive:  data/messages.jsonl

  #2518  2026-04-07 06:01  views=1  photos=3  | موسسه_معراج #گزارش_تصویری …
  #2521  2026-04-20 10:26  views=1  photos=2  | موسسه_معراج #گزارش_تصویری …
  …
```

### `bridge run` — continuous mode

Polls the channel on `source.poll_interval`, publishes new messages via the configured target, archives them, and updates the seen-set. Safe to stop and restart — already-published messages are skipped on the next run.

```sh
go run ./cmd/bridge run
```

Sample output:

```
source:   @Merajyan
target:   file:data/published.jsonl
interval: 5m0s
press Ctrl+C to stop
[09:14:54] published #2530 (.  بسم الله الرحمن الرحیم …)
[09:19:54] no new messages
```

---

## 5) Parsed message format

Every `Message` written to JSON has these fields:

| Field | Type | Notes |
|---|---|---|
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

---

## 6) Things still to be decided

Before the WordPress (or other) target can be wired up:

**Target site**

- [ ] Platform? (WordPress, custom, …)
- [ ] Site URL?
- [ ] Auth method? (Application Password, token, …)

**Post format**

- [ ] Each Eitaa message becomes a Post, Page, or Custom Post Type?
- [ ] Specific category / tag?
- [ ] Where does the title come from? (Eitaa messages have no title — first line or date?)
- [ ] How to handle images: re-upload to the site, or link back to eitaa.com?

**Operations**

- [ ] Where does it run? (Mac via launchd, VPS via systemd, …)

---

## 7) Known limitations

- **Unofficial.** Eitaa has no documented API; the parser depends on the public-channel HTML and will need updates if that markup changes.
- **Public channels only.** Private groups and channels are not accessible.
- **Media.** Photo URLs are captured, but downloading/re-uploading them to the destination site is not yet implemented.
- **Rate limits.** The default of one check every 5 minutes is intentionally conservative.

---

## License

Internal use — Meraj Cultural & Religious Institute.
