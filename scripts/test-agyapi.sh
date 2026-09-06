#!/bin/bash
# Exercises the agyapi backend through the running openmini server:
# 1) a 260 KB numbered-line prompt (asks which lines arrived), 2) a stream,
# 3) quota for the model used. Run: bash scripts/test-agyapi.sh [model]
B="${B:-http://localhost:18000}"
MODEL="${1:-agyapi/gemini-3.1-pro-low}"
T=$(mktemp -d)
python3 - "$T/big.json" "$MODEL" <<'PY'
import json,sys
lines=["L%04d: the quick brown fox jumps over the lazy dog." % i for i in range(1,5001)]
msg=("This message contains numbered lines labelled L0001 to L5000, one per line. Reply ONLY in this exact form and nothing else:\n"
     "first=<first label you see> last=<last label you see> missing=<the range of labels that is absent, or none>\n\n"+"\n".join(lines))
json.dump({"model":sys.argv[2],"messages":[{"role":"user","content":msg}]}, open(sys.argv[1],"w"))
print("prompt bytes:", len(msg.encode()))
PY
echo "=== 1. 260 KB prompt via $MODEL ==="
t0=$(date +%s)
curl -s -m 900 "$B/v1/chat/completions" -H 'Content-Type: application/json' --data-binary @"$T/big.json" -o "$T/big.out" -w 'http=%{http_code} '
echo "took $(( $(date +%s)-t0 ))s"
python3 -c "import json,sys; d=json.load(open('$T/big.out')); print('reply:', repr(d['choices'][0]['message']['content'])[:300]); print('usage:', d.get('usage'))"
echo "=== 2. stream via $MODEL ==="
curl -s -N -m 300 "$B/v1/chat/completions" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"List the four seasons as a markdown bullet list, then the word Done.\"}]}" -o "$T/stream.sse"
python3 - "$T/stream.sse" <<'PY'
import json,sys
raw=open(sys.argv[1],encoding='utf-8').read().split('\n')
com=[l for l in raw if l.startswith(': ')]; data=[l[6:] for l in raw if l.startswith('data: ')]
ch=[json.loads(l) for l in data if l!='[DONE]']
print('comments:', com); print('chunks:', len(ch), '| DONE:', '[DONE]' in data)
print('text:', repr(''.join(c['choices'][0]['delta'].get('content','') for c in ch)))
PY
echo "=== 3. quota for this model ==="
curl -s -m 60 "$B/usage?format=text" | grep "^agyapi: ${MODEL#agyapi/} "
rm -rf "$T"
