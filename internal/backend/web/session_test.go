package web

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
)

func pbLen(num int, data []byte) []byte {
	b := binary.AppendUvarint(nil, uint64(num<<3|2))
	b = binary.AppendUvarint(b, uint64(len(data)))
	return append(b, data...)
}

func pbVarint(num int, v uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(num<<3)), v)
}

func chunk(seq int, msg []byte) string {
	j := fmt.Sprintf(`[[%d,["%s"]]]`, seq, base64.StdEncoding.EncodeToString(msg))
	return fmt.Sprintf("%d %s", len(j), j)
}

func candidate(text string, status uint64) []byte {
	c := pbLen(1, []byte("rc_1234"))
	c = append(c, pbLen(2, pbLen(1, []byte(text)))...)
	c = append(c, pbLen(9, pbVarint(1, status))...)
	return append(pbLen(4, []byte("c_abc")), pbLen(5, c)...)
}

func TestReplyFromSession(t *testing.T) {
	body := `14 [[4,["noop"]]]` + chunk(5, candidate("Hello <b>wor", 1)) + chunk(6, candidate("Hello <b>world</b>", 2))
	text, final, ok := replyFromSession([]byte(body))
	if !ok || !final || text != "Hello <b>world</b>" {
		t.Errorf("got %q final=%v ok=%v", text, final, ok)
	}
	text, final, ok = replyFromSession([]byte(chunk(5, candidate("Hello <b>wor", 1))))
	if !ok || final || text != "Hello <b>wor" {
		t.Errorf("partial: got %q final=%v ok=%v", text, final, ok)
	}
	// a chunk without a candidate (title, progress note) is not a reply
	if _, _, ok := replyFromSession([]byte(chunk(7, pbLen(2, pbLen(1, []byte("rc_ but a plain string")))))); ok {
		t.Error("a message without the candidate shape was taken for a reply")
	}
	if _, _, ok := replyFromSession([]byte(`14 [[4,["noop"]]]`)); ok {
		t.Error("noop taken for a reply")
	}
}

// The files are processSession bodies captured from the app (Nov 2026) for
// the prompt "Reply exactly with this line and nothing else: ZEBRA-7731 <b>kept</b>",
// with location data and ids blanked out. The text arrives cumulatively.
func TestReplyFromSessionCaptured(t *testing.T) {
	cases := []struct {
		file, text string
		final      bool
	}{
		{"testdata/session-partial.txt", "ZEBRA-7731 ", false},
		{"testdata/session-final.txt", "ZEBRA-7731 <b>kept</b>", true},
	}
	for _, c := range cases {
		body, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		text, final, ok := replyFromSession(body)
		if !ok || text != c.text || final != c.final {
			t.Errorf("%s: got %q final=%v ok=%v; want %q final=%v", c.file, text, final, ok, c.text, c.final)
		}
	}
}
