# Latency across a day

Measured on 2026-09-09/10 with openmini 0.4.0 on a Windows 11 desktop, all three backends, one Google AI Pro
account. The point was Google's variance over the day rather than statistics of one moment, so the whole grid runs
once per cycle and the cycles are six hours apart, at about 08:00, 14:00, 20:00 and 02:00 local time (UTC+8).

**What was sent.** Five prompt sizes, 10k, 30k, 100k, 200k and 300k characters of English prose and source
code, identical for every model and cycle at a given size, each ending with "Summarize the material above in one
sentence." so the reply is short and the numbers measure the input path and the queue, not generation length.
Every `/v1` model took part except gpt-oss; Claude Sonnet and Opus only up to 100k, agy only up to 100k because
larger prompts are rerouted to agyapi anyway. Web requests used the paste path up to 100k and the file path
above it, each in a temporary chat. All requests were streamed.

**What the numbers are.** Each cell is seconds from sending the request to the last byte of the reply, to the
hundredth of a second. With a one-sentence answer the reply itself takes well under a second on every path, so
this is the time to get an answer at all. Before every request a fixed probe, a 1k prompt
on `agyapi/gemini-3.6-flash`, was timed as a gauge of the Antigravity service at that moment; its median and
tail are given per cycle. A request with nothing back after fifteen minutes would have been recorded as `silent`.

**Caveats.** One sample per cell per cycle, so a single number is a reading, not an average. The first agy call of
a cycle includes agy's own cold start. The 200k and 300k prompts repeat the 150k text a second time. The probe
measures the Antigravity service; the web app is a different service, so for web it is only a proxy for how
loaded Google is.

## Results

### About 08:00

Not run yet.

### About 14:00

49 requests; 49 ok. Probe before each request: median 1.78 s, 90th percentile 5.52 s, slowest 14.71 s.

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

Concurrency, N identical 10k requests at once on `gemini-3.6-flash`; batch wall time, then each request:

| backend | N | batch | each |
|---|---:|---:|---|
| agyapi | 1 | 1.38 s | 1.38 |
| agyapi | 2 | 1.89 s | 1.51, 1.88 |
| agyapi | 4 | 5.10 s | 1.45, 1.76, 2.81, 5.10 |
| agyapi | 8 | 1.86 s | 1.43, 1.55, 1.57, 1.65, 1.72, 1.74, 1.84, 1.86 |
| agy | 1 | 9.58 s | 9.58 |
| agy | 2 | 13.05 s | 7.49, 13.05 |
| agy | 4 | 33.44 s | 6.69, 19.32, 24.06, 33.43 |
| agy | 8 | 51.26 s | 4.39, 8.19, 13.85, 25.47, 31.41, 37.23, 42.49, 51.26 |

### About 20:00

Not run yet.

### About 02:00

Not run yet.

## Reading them

- **Size hardly matters when Google is quick.** At 14:00 agyapi answered a 300k prompt in the same few seconds
  as a 10k one, and the web file path at 200k and 300k was as fast as pasting 30k.
- **agyapi is the fastest path**; agy adds a steady overhead on top of the same service, plus a cold start on its
  first call.
- **Only agyapi runs requests in parallel.** Eight at once finished together; on agy eight at once finished one
  after another at roughly six-second intervals, so agy requests queue behind each other.
- The differences between cycles are Google's, not openmini's: the same bytes went in every time.
