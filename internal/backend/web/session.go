package web

import (
	"encoding/base64"
	"encoding/binary"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Since late 2026 the Gemini app streams its replies through
// geminiweb-pa.clients6.google.com/v1/processSession instead of StreamGenerate.
// Each response body holds length-prefixed JSON chunks, [[seq,["<base64>"]]],
// and the base64 is a protobuf message. Somewhere inside it sits the reply
// candidate: field 1 its id ("rc_..."), field 2 a message whose field 1 is the
// reply text as the model wrote it, and field 9 a message whose field 1 is the
// status (2 once the reply is complete). The text is cumulative.

var sessionChunkRe = regexp.MustCompile(`\[\[\d+,\["([A-Za-z0-9+/=_-]+)"\]\]\]`)

// replyFromSession returns the last reply text found in a processSession
// body and whether that chunk marked the reply complete.
func replyFromSession(body []byte) (text string, final, found bool) {
	for _, m := range sessionChunkRe.FindAllSubmatch(body, -1) {
		b64 := string(m[1])
		if b64 == "noop" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64 + strings.Repeat("=", (4-len(b64)%4)%4))
		if err != nil {
			if raw, err = base64.URLEncoding.DecodeString(b64 + strings.Repeat("=", (4-len(b64)%4)%4)); err != nil {
				continue
			}
		}
		if t, status, ok := findCandidate(raw, 0); ok {
			text, final, found = t, status == 2, true
		}
	}
	return text, final, found
}

// pbField is one field of a protobuf message: a varint, or a length-delimited
// value (string, bytes or a nested message). Fixed-width fields are skipped.
type pbField struct {
	num    uint64
	varint uint64
	data   []byte
	isLen  bool
}

// pbParse splits b into fields; ok is false when b is not a well-formed
// message, which is how a string is told apart from a nested message.
func pbParse(b []byte) (fields []pbField, ok bool) {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, false
		}
		b = b[n:]
		f := pbField{num: key >> 3}
		if f.num == 0 {
			return nil, false
		}
		switch key & 7 {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, false
			}
			f.varint, b = v, b[n:]
		case 1:
			if len(b) < 8 {
				return nil, false
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return nil, false
			}
			f.data, f.isLen, b = b[n:n+int(l)], true, b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return nil, false
			}
			b = b[4:]
		default:
			return nil, false
		}
		fields = append(fields, f)
	}
	return fields, true
}

// findCandidate searches a message and its nested messages for the reply
// candidate and returns its text and status.
func findCandidate(b []byte, depth int) (text string, status uint64, ok bool) {
	if depth > 10 {
		return "", 0, false
	}
	fields, valid := pbParse(b)
	if !valid {
		return "", 0, false
	}
	var id, body, state []byte
	for _, f := range fields {
		switch {
		case f.num == 1 && f.isLen:
			id = f.data
		case f.num == 2 && f.isLen:
			body = f.data
		case f.num == 9 && f.isLen:
			state = f.data
		}
	}
	if inner, parsed := pbParse(body); strings.HasPrefix(string(id), "rc_") && body != nil && parsed {
		for _, f := range inner {
			if f.num == 1 && f.isLen && utf8.Valid(f.data) {
				text = string(f.data)
			}
		}
		if st, parsed := pbParse(state); parsed {
			for _, f := range st {
				if f.num == 1 && !f.isLen {
					status = f.varint
				}
			}
		}
		return text, status, true
	}
	for _, f := range fields {
		if f.isLen {
			if t, s, ok := findCandidate(f.data, depth+1); ok {
				return t, s, true
			}
		}
	}
	return "", 0, false
}
