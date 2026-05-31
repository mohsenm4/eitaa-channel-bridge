# Eitaa Channel Bridge

> A small bot that automatically reads every new message published in the Eitaa channel of **Meraj Cultural & Religious Institute** ([@Merajyan](https://eitaa.com/Merajyan)) and republishes it on a target website in a defined format.

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
| **Reading messages from the Eitaa channel** | ✅ Feasible | The channel is public and the `eitaa.com/Merajyan` page contains the full message text in HTML. Each message has a clear ID (`/s/Merajyan/2518`) which is ideal for distinguishing "seen" vs. "new". |
| **Publishing to the site** | ⚠️ Depends | Depends on the target site — if it is WordPress, the REST API makes it straightforward; if it is a custom site, it needs an endpoint. (More info required.) |
| **Scheduling** | ✅ Feasible | Can be run via `cron` on a server or on the same Mac. |

**Conclusion:** The whole project is doable; only the "target site" part needs more information (see section 5).

---

## 3) Proposed Architecture

Project components:

```
eitaa-channel-bridge/
├── README.md                  ← this file
├── .gitignore
├── requirements.txt           ← Python dependencies
├── config.yaml.example        ← sample configuration (no secrets)
├── bridge/
│   ├── __init__.py
│   ├── eitaa_reader.py        ← reads and parses Eitaa channel HTML
│   ├── state.py               ← tracks the last seen message ID
│   ├── formatter.py           ← converts messages to the target site format
│   ├── publisher.py           ← posts to the site (WordPress/…)
│   └── main.py                ← main entry point
├── data/
│   ├── seen.json              ← IDs of seen messages (ignored)
│   └── messages.jsonl         ← raw message archive (ignored)
└── tests/
```

**Suggested language:** Python — because of its mature libraries for HTML parsing and API calls.

---

## 4) Message Format

For each message bridged from Eitaa to the site, these fields are available:

| Field | Source | Example |
|---|---|---|
| `id` | message URL | `2518` |
| `text` | message text | "Weekly schedule 🟩 Creativity | Play" |
| `date` | timestamp | `1769472060` |
| `views` | view counter | `42 views` |
| `link` | message link on Eitaa | `https://eitaa.com/Merajyan/2518` |
| `media` | image/file (if any) | file URL |

**The final post format on the site** is not yet defined — it will be specified in the next discussion.

---

## 5) Things still to be decided

Before writing the code, these questions need answers:

### a) About the target site:
- [ ] What platform is the site running on? (WordPress, Joomla, custom, …)
- [ ] Site URL?
- [ ] Does it expose an API? (e.g. WordPress REST API)
- [ ] What is the authentication method for publishing posts? (Application Password, Token, …)

### b) About the post format on the site:
- [ ] What type of content should each Eitaa message become? (Post, Page, Custom Post Type)
- [ ] Should it have a specific category or tag?
- [ ] Where should the post title come from? (Eitaa messages do not have an independent title — the first line or the date can be used.)
- [ ] If the message contains an image or file, how should it be transferred?

### c) About execution:
- [ ] How often should it check? (default: 5 minutes)
- [ ] Which server should run it? (your Mac / a VPS / …)

---

## 6) Known Limitations

- **Unofficial:** Eitaa does not provide an official API for reading messages. This approach depends on the HTML structure of the Eitaa site; if Eitaa ever changes its site structure, the code will need updates.
- **Public channels only:** This approach does not work on private groups or channels.
- **Media:** Downloading images/files from Eitaa and re-uploading them to the site requires extra work.
- **Rate limit:** Sending too many requests too quickly may get our IP temporarily blocked. The default of one check every 5 minutes is conservative.

---

## 7) Next Steps

1. ✅ Confirm that the channel is readable.
2. ✅ Set up the project skeleton (this commit).
3. ⏳ Answer the questions in section 5 (target site, format).
4. ⏳ Write `eitaa_reader.py` and test reading messages.
5. ⏳ Write `publisher.py` tailored to the target site.
6. ⏳ Connect the two and run end-to-end tests.
7. ⏳ Set up on cron.

---

## License

Internal use — Meraj Cultural & Religious Institute.
