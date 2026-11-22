package files

import (
	"fmt"
	"strconv"
	"strings"
)

// Key is the key a managed file stands at in the family model: its
// slash-separated path with each byte a family key may not hold written as _
// and two hex digits, so docs/a_b.md stands at docs_2fa_5fb.md. Letters,
// digits, dots and dashes stay as they are.
func Key(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c == '-', c == '.', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "_%02x", c)
		}
	}
	return b.String()
}

// PathOf is the path a key of the family model names, the inverse of Key; a
// key Key never writes is refused.
func PathOf(key string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(key); i++ {
		if key[i] != '_' {
			b.WriteByte(key[i])
			continue
		}
		if i+2 >= len(key) {
			return "", fmt.Errorf("files: key %q ends inside an escape: a key escapes a byte as _ and two hex digits", key)
		}
		n, err := strconv.ParseUint(key[i+1:i+3], 16, 8)
		if err != nil {
			return "", fmt.Errorf("files: key %q escapes %q, which is no two hex digits", key, key[i+1:i+3])
		}
		b.WriteByte(byte(n))
		i += 2
	}
	return b.String(), nil
}
