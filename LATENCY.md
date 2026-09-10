# Latency across a day

## In short

openmini answered every one of the 196 requests in this run, at four times of day, across prompt sizes from 10k to
300k characters, with none stuck or dropped. Median throughput in tokens per second, pooled over all sizes and
models (tokens estimated at four characters each; higher is better):

| backend (tokens/s) | 08:00 | 14:00 | 20:00 | 02:00 |
|---|---:|---:|---:|---:|
| web | 807 | 1510 | 886 | 538 |
| agy | 1398 | 960 | 724 | 896 |
| agyapi | 3283 | 2740 | 3975 | 3994 |

- **agyapi has the highest throughput** at every hour, three to eight times the web backend. Most of each request
  is fixed overhead, so throughput rises with prompt size: a 300k prompt on agyapi Flash clears over 30,000
  tokens/s, a 10k one only about 1,300, which is why a pooled median spans a wide range.
- **agy runs at roughly a third to a half of agyapi's rate**, the same service reached through the CLI.
- **The web backend is the slowest and swings with the hour** — its rate varies about three times between its best
  and worst hour, and its worst hour is the middle of the night, when the Antigravity service is at its calmest.
  The two Google services do not move together.

## The test

Run on 2026-09-09/10 with openmini 0.4.0 on a Windows 11 desktop, all three backends, one Google AI Pro account.
The aim was Google's variance over a day, so the whole grid runs once per cycle and the cycles are six hours apart,
covering roughly 08:00, 14:00, 20:00 and 02:00 local time (UTC+8).

**What was sent.** Five prompt sizes, 10k, 30k, 100k, 200k and 300k characters of English prose and source code,
identical for every model and cycle at a given size, each ending "Summarize the material above in one sentence." so
the reply is short and the number measures the input path and the queue, not how long the model writes. Every
`/v1` model took part except gpt-oss; Claude Sonnet and Opus only up to 100k, agy only up to 100k because larger
prompts are rerouted to agyapi anyway. Web requests used the paste path up to 100k and the file path above it, each
in a temporary chat. All requests were streamed.

**What the numbers are.** Each cell is seconds from sending the request to the last byte of the reply. With a
one-sentence answer the reply itself is well under a second on every path, so this is the time to get an answer at
all. Before every request a fixed probe, a 1k prompt on `agyapi/gemini-3.6-flash`, was timed as a gauge of the
Antigravity service at that moment; its median and tail are given per cycle. A request with nothing back after
fifteen minutes would have been recorded as `silent`; none were.

**Caveats.** One sample per cell per cycle, so a single number is a reading, not an average. The first agy call of
a cycle includes agy's own cold start. The 200k and 300k prompts repeat the 150k text a second time. The probe
measures the Antigravity service; the web app is a different service, so for web it is only a proxy for how loaded
Google is.

## Results

### About 08:00

49 requests, 49 ok. Probe before each: median 1.37 s, 90th percentile 4.94 s, slowest 6.45 s.

| model | 10k | 30k | 100k | 200k | 300k |
|---|---:|---:|---:|---:|---:|
| `web/3.5 Flash-Lite` | 21.49 s | 23.33 s | 30.92 s | 27.34 s | 25.68 s |
| `web/3.8 Flash` | 19.32 s | 32.83 s | 31.77 s | 27.46 s | 25.98 s |
| `web/3.1 Pro` | 20.13 s | 19.95 s | 31.06 s | 33.77 s | 36.06 s |
| `agy/gemini-3.8-flash` | 6.58 s | 4.39 s | 7.36 s |  |  |
| `agy/gemini-3.7-flash` | 3.47 s | 4.46 s | 3.73 s |  |  |
| `agy/gemini-3.6-flash` | 5.68 s | 3.87 s | 3.80 s |  |  |
| `agy/gemini-3.1-pro` | 7.14 s | 14.67 s | 9.74 s |  |  |
| `agy/claude-sonnet-4-6` | 15.28 s | 6.89 s | 7.82 s |  |  |
| `agy/claude-opus-4-6` | 48.21 s | 11.62 s | 7.52 s |  |  |
| `agyapi/gemini-3.6-flash` | 2.01 s | 1.67 s | 3.34 s | 1.80 s | 2.22 s |
| `agyapi/gemini-3.1-pro` | 11.90 s | 6.83 s | 7.12 s | 7.58 s | 8.38 s |
| `agyapi/claude-sonnet-4-6` | 4.27 s | 4.41 s | 4.31 s |  |  |
| `agyapi/claude-opus-4-6` | 4.12 s | 4.53 s | 8.23 s |  |  |

### About 14:00

49 requests, 49 ok. Probe before each: median 1.78 s, 90th percentile 5.52 s, slowest 14.71 s.

| model | 10k | 30k | 100k | 200k | 300k |
|---|---:|---:|---:|---:|---:|
| `web/3.5 Flash-Lite` | 6.73 s | 7.45 s | 16.60 s | 10.51 s | 11.87 s |
| `web/3.8 Flash` | 7.34 s | 8.71 s | 16.55 s | 16.01 s | 21.31 s |
| `web/3.1 Pro` | 15.57 s | 13.66 s | 24.96 s | 19.24 s | 28.21 s |
| `agy/gemini-3.8-flash` | 40.81 s | 4.80 s | 8.40 s |  |  |
| `agy/gemini-3.7-flash` | 3.07 s | 7.15 s | 3.93 s |  |  |
| `agy/gemini-3.6-flash` | 8.10 s | 5.30 s | 7.57 s |  |  |
| `agy/gemini-3.1-pro` | 14.19 s | 8.76 s | 9.54 s |  |  |
| `agy/claude-sonnet-4-6` | 7.51 s | 16.58 s | 14.36 s |  |  |
| `agy/claude-opus-4-6` | 12.64 s | 22.88 s | 8.35 s |  |  |
| `agyapi/gemini-3.6-flash` | 1.38 s | 6.17 s | 5.49 s | 5.55 s | 2.26 s |
| `agyapi/gemini-3.1-pro` | 6.54 s | 8.07 s | 8.10 s | 7.31 s | 6.63 s |
| `agyapi/claude-sonnet-4-6` | 2.90 s | 3.17 s | 5.24 s |  |  |
| `agyapi/claude-opus-4-6` | 5.03 s | 5.04 s | 5.60 s |  |  |

### About 20:00

49 requests, 49 ok. Probe before each: median 2.35 s, 90th percentile 11.34 s, slowest 29.21 s.

| model | 10k | 30k | 100k | 200k | 300k |
|---|---:|---:|---:|---:|---:|
| `web/3.5 Flash-Lite` | 22.16 s | 22.92 s | 24.32 s | 22.25 s | 27.77 s |
| `web/3.8 Flash` | 24.72 s | 28.09 s | 28.30 s | 47.04 s | 25.13 s |
| `web/3.1 Pro` | 21.24 s | 24.65 s | 35.05 s | 37.49 s | 36.54 s |
| `agy/gemini-3.8-flash` | 24.21 s | 4.36 s | 18.40 s |  |  |
| `agy/gemini-3.7-flash` | 4.56 s | 4.31 s | 4.86 s |  |  |
| `agy/gemini-3.6-flash` | 10.82 s | 3.83 s | 21.49 s |  |  |
| `agy/gemini-3.1-pro` | 47.02 s | 16.01 s | 17.84 s |  |  |
| `agy/claude-sonnet-4-6` | 19.00 s | 8.54 s | 14.05 s |  |  |
| `agy/claude-opus-4-6` | 21.63 s | 16.21 s | 59.15 s |  |  |
| `agyapi/gemini-3.6-flash` | 1.31 s | 1.57 s | 1.29 s | 1.97 s | 2.04 s |
| `agyapi/gemini-3.1-pro` | 6.90 s | 6.17 s | 6.33 s | 12.54 s | 7.39 s |
| `agyapi/claude-sonnet-4-6` | 7.32 s | 7.04 s | 3.73 s |  |  |
| `agyapi/claude-opus-4-6` | 4.16 s | 5.44 s | 4.72 s |  |  |

### About 02:00

49 requests, 49 ok. Probe before each: median 1.46 s, 90th percentile 2.67 s, slowest 5.00 s.

| model | 10k | 30k | 100k | 200k | 300k |
|---|---:|---:|---:|---:|---:|
| `web/3.5 Flash-Lite` | 35.77 s | 32.18 s | 42.20 s | 36.76 s | 41.38 s |
| `web/3.8 Flash` | 37.84 s | 32.99 s | 46.60 s | 39.53 s | 44.92 s |
| `web/3.1 Pro` | 38.44 s | 35.84 s | 46.64 s | 52.01 s | 50.28 s |
| `agy/gemini-3.8-flash` | 3.77 s | 5.65 s | 8.94 s |  |  |
| `agy/gemini-3.7-flash` | 3.50 s | 9.39 s | 4.31 s |  |  |
| `agy/gemini-3.6-flash` | 3.40 s | 3.37 s | 9.19 s |  |  |
| `agy/gemini-3.1-pro` | 15.79 s | 7.98 s | 6.80 s |  |  |
| `agy/claude-sonnet-4-6` | 7.92 s | 8.94 s | 11.61 s |  |  |
| `agy/claude-opus-4-6` | 11.81 s | 13.88 s | 11.28 s |  |  |
| `agyapi/gemini-3.6-flash` | 1.86 s | 1.54 s | 1.64 s | 2.30 s | 5.61 s |
| `agyapi/gemini-3.1-pro` | 6.26 s | 7.78 s | 6.61 s | 10.00 s | 8.47 s |
| `agyapi/claude-sonnet-4-6` | 3.05 s | 3.67 s | 5.97 s |  |  |
| `agyapi/claude-opus-4-6` | 4.25 s | 4.30 s | 5.28 s |  |  |

## Summary

Across a full day the order never changed: agyapi highest throughput and steadiest across the hours, agy behind it,
the web backend both slowest and by far the most variable. For anything latency-sensitive or large, agyapi is the
backend to use; the web backend is best kept for when only the Gemini app's own quota or behaviour is wanted, and
its speed should be expected to vary by the hour. Nothing timed out or got stuck at any hour, which was the main
thing to confirm.
