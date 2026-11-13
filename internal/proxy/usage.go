package proxy

import (
	"bytes"
	"encoding/json"
)

const usageTailBytes = 4 << 10

// tailBuf remembers the last 4 KB of a stream. The usage object is at the end,
// and keeping only this window means a long completion is never stored.
type tailBuf struct {
	buf [usageTailBytes]byte
	n   int
	pos int
}

func (t *tailBuf) write(p []byte) {
	if len(p) == 0 {
		return
	}
	if len(p) >= len(t.buf) {
		copy(t.buf[:], p[len(p)-len(t.buf):])
		t.n = len(t.buf)
		t.pos = 0
		return
	}
	for len(p) > 0 {
		n := len(t.buf) - t.pos
		if n > len(p) {
			n = len(p)
		}
		copy(t.buf[t.pos:t.pos+n], p[:n])
		p = p[n:]
		t.pos += n
		if t.pos == len(t.buf) {
			t.pos = 0
		}
		t.n += n
		if t.n > len(t.buf) {
			t.n = len(t.buf)
		}
	}
}

func (t *tailBuf) bytes() []byte {
	if t.n == 0 {
		return nil
	}
	out := make([]byte, t.n)
	if t.n < len(t.buf) {
		copy(out, t.buf[:t.n])
		return out
	}
	n := copy(out, t.buf[t.pos:])
	copy(out[n:], t.buf[:t.pos])
	return out
}

// actualTokens reads the last usage object in the tail. total_tokens is the
// whole call; prompt plus completion covers a usage object that omits the sum.
func actualTokens(tail []byte) (int, bool) {
	idx := bytes.LastIndex(tail, []byte(`"usage"`))
	if idx < 0 {
		return 0, false
	}
	rest := bytes.TrimLeft(tail[idx+len(`"usage"`):], " \t\r\n")
	rest = bytes.TrimPrefix(rest, []byte(":"))
	rest = bytes.TrimLeft(rest, " \t\r\n")
	if len(rest) == 0 || rest[0] == 'n' {
		return 0, false
	}
	var usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	}
	if err := json.NewDecoder(bytes.NewReader(rest)).Decode(&usage); err != nil {
		return 0, false
	}
	if usage.TotalTokens > 0 {
		return usage.TotalTokens, true
	}
	return usage.PromptTokens + usage.CompletionTokens, true
}
