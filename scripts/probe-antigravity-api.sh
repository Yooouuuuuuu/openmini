#!/bin/bash
# Calls the Antigravity backend endpoints directly with agy's stored token and
# prints only non-secret results: tier, model ids, quota fields, one generation.
# Run it yourself: bash scripts/probe-antigravity-api.sh
set -u
TOKEN_FILE="${TOKEN_FILE:-$HOME/.gemini/antigravity-cli/antigravity-oauth-token}"
UA="${UA:-antigravity/1.13.0 linux/amd64}"
B=https://cloudcode-pa.googleapis.com
OUT=$(mktemp -d)
TOK=$(python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['token']['access_token'])" "$TOKEN_FILE") || { echo "cannot read token file $TOKEN_FILE"; exit 1; }
H=(-H "Authorization: Bearer $TOK" -H "Content-Type: application/json" -H "User-Agent: $UA")

echo "=== loadCodeAssist ==="
curl -s -m 30 "${H[@]}" -X POST "$B/v1internal:loadCodeAssist" \
  -d '{"metadata":{"ideType":"ANTIGRAVITY","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}}' -o "$OUT/lca.json" -w 'http=%{http_code}\n'
PROJ=$(python3 - "$OUT/lca.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1])); p=d.get('cloudaicompanionProject') or ''
print("keys:", list(d.keys()), file=sys.stderr)
print("currentTier:", (d.get('currentTier') or {}).get('id'), "| paidTier:", (d.get('paidTier') or {}).get('id'), "| project set:", bool(p), file=sys.stderr)
print(p)
PY
)

echo "=== fetchAvailableModels ==="
curl -s -m 30 "${H[@]}" -X POST "$B/v1internal:fetchAvailableModels" -d "{\"project\":\"$PROJ\"}" -o "$OUT/models.json" -w 'http=%{http_code}\n'
python3 - "$OUT/models.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
print("keys:", list(d.keys())[:8])
ms=d.get('models') or d.get('availableModels') or []
if isinstance(ms, dict): ms=[dict(v, _id=k) for k,v in ms.items()]
print("models:", len(ms))
for m in ms[:25]: print("  ", json.dumps(m, ensure_ascii=False)[:220])
PY

echo "=== retrieveUserQuota ==="
curl -s -m 30 "${H[@]}" -X POST "$B/v1internal:retrieveUserQuota" -d "{\"project\":\"$PROJ\"}" -o "$OUT/quota.json" -w 'http=%{http_code}\n'
head -c 1200 "$OUT/quota.json"; echo

echo "=== streamGenerateContent: one word on gemini-3.1-pro ==="
curl -s -m 90 "${H[@]}" -X POST "$B/v1internal:streamGenerateContent?alt=sse" \
  -d "{\"model\":\"gemini-3.1-pro\",\"project\":\"$PROJ\",\"request\":{\"contents\":[{\"role\":\"user\",\"parts\":[{\"text\":\"Reply with one word: ready\"}]}]}}" -o "$OUT/gen.sse" -w 'http=%{http_code}\n'
head -c 1500 "$OUT/gen.sse"; echo
echo "(raw files in $OUT; delete when done)"
