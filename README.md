# Eitaa Channel Bridge

> A small bot that reads every new message published in the Eitaa channel of **Meraj Cultural & Religious Institute** ([@Merajyan](https://eitaa.com/Merajyan)) and republishes it on a target website in a defined format.

---

## 1) Project Goal

This bot acts like an **automated reporter**:

1. Every few minutes it checks the Eitaa channel.
2. It identifies new messages (those it has not seen before).
3. It publishes each new message on the target site using a predefined template.
4. It logs everything it has published so the same message is never sent twice.

```
┌───────────────┐      ┌──────────────┐      ┌──────────────┐
│ Eitaa channel │  →   │  Bridge bot   │  →   │ Target site  │
│ @Merajyan     │      │ (this project)│      │ (WordPress/…) │
└───────────────┘      └──────────────┘      └──────────────┘
```

---

## 2) Is it feasible?

| Part | Status | Notes |
|---|---|---|
| **Reading messages from the Eitaa channel** | Yes | The channel is public and `eitaa.com/Merajyan` returns the full message stream as HTML. Each message has a stable ID (e.g. `/Merajyan/2518`) which is ideal for distinguishing "seen" vs. "new". |
| **Publishing to the site** | Depends | Depends on the target site — if it is WordPress, the REST API makes it straightforward; if it is a custom site, it needs an endpoint. (More info required.) |
| **Scheduling** | Yes | Can be run via `cron` / `launchd` / `systemd`, or just `bridge poll` in the foreground. |

**Conclusion:** The whole project is doable; only the "target site" part needs more information (see section 5).

---

## 3) Architecture

**Language:** Go — the parser uses only the standard library plus `golang.org/x/net/html`, so the bridge ships as a single static binary with no runtime dependencies.

```
eitaa-channel-bridge/
├── README.md
├── .gitignore
├── go.mod
├── go.sum
├── config.yaml.example
├── cmd/
│   └── bridge/
│       └── main.go              ← CLI entry point (dump, poll)
├── internal/
│   ├── eitaa/
│   │   └── reader.go            ← fetches and parses the channel HTML
│   └── state/
│       └── state.go             ← tracks seen message IDs
└── data/
    ├── seen.json                ← IDs of processed messages (gitignored)
    ├── messages.jsonl           ← raw message archive (gitignored)
    ├── last_dump.json           ← last dump pretty JSON (gitignored)
    └── raw.html                 ← last raw HTML response (gitignored)
```

---

## 4) Commands

Build:

```sh
go build -o bin/bridge ./cmd/bridge
```

### `bridge dump` — one-shot inspection

Fetches the channel once, writes the raw HTML and parsed JSON to `data/`, and prints a summary of every message it found. Use this to inspect the structure of Eitaa messages and verify the parser.

```sh
go run ./cmd/bridge dump
# or with a different channel
go run ./cmd/bridge dump --channel SomeOtherChannel --data-dir ./out
```

Sample output:

```
channel:   @Merajyan
messages:  6
raw html:  data/raw.html
pretty:    data/last_dump.json
jsonl:     data/messages.jsonl

  #2518  2026-04-07 06:01  views=1  photos=3  | موسسه_معراج #گزارش_تصویری …
  #2521  2026-04-20 10:26  views=1  photos=2  | موسسه_معراج #گزارش_تصویری …
  …
```

### `bridge poll` — continuous mode

Fetches on an interval, appends only new messages to `data/messages.jsonl`, and persists the seen-set in `data/seen.json`. Safe to stop and restart — already-published messages will not be re-sent.

```sh
go run ./cmd/bridge poll --interval 5m
```

---

## 5) Message format (parsed)

Every message dumped to JSON has these fields:

| Field | Type | Notes |
|---|---|---|
| `id` | int | the numeric message ID inside the channel |
| `channel` | string | channel username, without `@` |
| `link` | string | canonical URL on eitaa.com |
| `author` | string | post author / owner name |
| `forwarded_from` | string | only set when the message was forwarded |
| `date` | RFC3339 timestamp | from the `<time datetime=…>` attribute |
| `views` | int | view counter |
| `text` | string | plain text with newlines preserved |
| `text_html` | string | inner HTML of the message bubble (hashtags as `<a>` links) |
| `photos` | string[] | URLs of attached photos (empty for text-only posts) |

The **final post format on the target site** is not yet defined — it will be specified after the questions in section 6 are answered.

---

## 6) Things still to be decided

Before writing the publisher, these questions need answers:

**a) About the target site**

- [ ] What platform is the site running on? (WordPress, Joomla, custom, …)
- [ ] Site URL?
- [ ] Does it expose an API? (e.g. WordPress REST API)
- [ ] What is the authentication method for publishing posts? (Application Password, Token, …)

**b) About the post format**

- [ ] What type of content should each Eitaa message become? (Post, Page, Custom Post Type)
- [ ] Should it have a specific category or tag?
- [ ] Where should the post title come from? (Eitaa messages do not have an independent title — the first line or the date can be used.)
- [ ] If the message contains an image or file, how should it be transferred?

**c) About execution**

- [ ] How often should it check? (default: 5 minutes)
- [ ] Which server should run it? (your Mac / a VPS / …)

---

## 7) Known Limitations

- **Unofficial:** Eitaa does not provide an official API for reading messages. This approach depends on the HTML structure of the Eitaa site; if Eitaa changes its markup, the parser will need updates.
- **Public channels only:** Private groups and channels are not accessible.
- **Media:** Downloading images/files from Eitaa and re-uploading them to the target site requires extra work and is not implemented yet.
- **Rate limit:** Sending too many requests too quickly may get our IP temporarily blocked. The default of one check every 5 minutes is conservative.

---

## 8) Next Steps

1. Confirm that the channel is readable.
2. Set up the project skeleton.
3. Implement the parser (`internal/eitaa`) and validate against the live channel (`bridge dump`).
4. Answer the questions in section 6 (target site, format).
5. Implement `internal/publisher` for the chosen site.
6. Wire `bridge poll` into the publisher and run end-to-end tests.
7. Deploy on cron / launchd / systemd.

---

## License

Internal use — Meraj Cultural & Religious Institute.
