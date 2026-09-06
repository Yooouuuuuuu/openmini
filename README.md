# openmini

Your Google AI subscription as an OpenAI-compatible endpoint. Two backends behind one port:

- **web**: drives gemini.google.com in a signed-in browser (the Gemini app quota).
- **agyapi**: calls the Antigravity backend service directly with agy's signed-in token (the Antigravity quota,
  per-model usage, no message cap, real token counts). The recommended backend for long prompts.
- **agy**: runs the Antigravity CLI with a tool-less agent (same quota, but the harness caps messages at 192,000
  bytes and adds ~10k tokens of its own prompt per call).

```bash
curl http://localhost:18000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "web/3.1 Pro",
       "messages": [{"role": "user", "content": "Write a two-sentence story about a cat who learns to sail."}]}'
```

Model names pick the backend: `agyapi/gemini-3.1-pro-low`, `agyapi/claude-sonnet-4-6`, `web/3.1 Pro`, `agy/gemini-3.1-pro-low`,
or `web/default` and `agy/default`. A bare name that exists in exactly one backend routes there; anything else goes
to `default_backend`. `GET /v1/models` lists everything. Add `"stream": true` for server-sent events; the first
chunk acknowledges the request and `: openmini phase=...` comments report submitted and generating before the text.

| Endpoint | Purpose |
|---|---|
| `GET /usage` | the dashboard: one tile per backend with its own Refresh button, running requests with Stop; nothing is fetched on open except cached values (`/` redirects here) |
| `GET /usage/cached`, `POST /usage/refresh?backend=` | the cache behind that page; refresh asks one backend |
| `POST /v1/chat/completions` | OpenAI chat completions |
| `GET /v1/models` | all backend models, prefixed |
| `GET /status` (`?format=text`) | what every request is doing: queued, submitted, generating, with elapsed time and characters so far |
| `GET /status/stream` | the same, pushed as server-sent events whenever a request changes; what the dashboard listens to (no polling) |
| `POST /requests/stop?id=` | cancel a running request |

Request phases: `queued`, `submitted`, `thinking` (web only: the page shows the model thinking and no text yet), `generating`, then `finished`, `failed` or `stopped`. The dashboard at `/` shows them as a timeline with a Stop button.
| `GET /health` | backend readiness |
| `GET /debug/web/html`, `/debug/web/screenshot` | the live Gemini page, for fixing selectors |

## Setup

```bash
./run.sh            # creates config.toml from the template on first run, builds, runs doctor, asks, starts tmux
./openmini doctor   # checks alone
./openmini status   # request state
```

Sign-in is done once by hand: for the web backend set `headless = false`, start, sign in to Google in the window,
then switch back to headless; for agy and agyapi run `agy` once in a terminal (agyapi reuses agy's token file and
lets agy refresh it). `config.toml` is yours and not committed;
`config.example.toml` is the template. Logs go to `logs/<date>.log`, one file per day, old ones removed; they hold
ids, sizes and timings, never prompt or reply text.

## Notes

- Size limits: the Gemini web box refuses single lines over ~32k characters, pasted prompts arrive intact up to ~100k characters
  and larger ones go as a file attachment; agy silently drops everything after the first 192,000 bytes of a message (about 100k Chinese or
  190k English characters), verified with a numbered-line test; openmini reroutes, reports or refuses such prompts (`oversize_action`).
- Gemini's own error notices and refusals are passed through as the reply. Failures to get any reply come back as
  content prefixed `[openmini/<backend>]`, never as HTTP errors, except malformed requests and bad API keys.
- Set `api_keys` in config.toml before exposing the port beyond localhost or your tailnet.
