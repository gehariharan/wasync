package passkey

import (
	"errors"
	"math"
)

// A small CBOR reader (RFC 8949) for the two CBOR structures WebAuthn hands a
// server: the attestation object and the COSE public key inside it.
// Authenticators encode both with definite lengths, so indefinite lengths are
// refused, as are nesting deeper than any real key needs and duplicate map keys.

var errCBOR = errors.New("malformed CBOR")

const cborMaxDepth = 8

// decodeCBOR reads one data item and returns it with the bytes after it.
// Integers become int64, byte strings []byte, text strings string, arrays
// []any and maps map[any]any (keys int64 or string). Tags are dropped and
// simple values and floats become nil; nothing wasync reads uses them.
func decodeCBOR(b []byte) (any, []byte, error) { return cborItem(b, 0) }

func cborItem(b []byte, depth int) (any, []byte, error) {
	if depth > cborMaxDepth || len(b) == 0 {
		return nil, nil, errCBOR
	}
	major, info := b[0]>>5, b[0]&0x1f
	b = b[1:]
	var arg uint64
	switch {
	case info < 24:
		arg = uint64(info)
	case info <= 27:
		n := 1 << (info - 24)
		if len(b) < n {
			return nil, nil, errCBOR
		}
		for _, c := range b[:n] {
			arg = arg<<8 | uint64(c)
		}
		b = b[n:]
	default:
		return nil, nil, errCBOR // reserved, or an indefinite length
	}
	switch major {
	case 0:
		if arg > math.MaxInt64 {
			return nil, nil, errCBOR
		}
		return int64(arg), b, nil
	case 1:
		if arg > math.MaxInt64 {
			return nil, nil, errCBOR
		}
		return -1 - int64(arg), b, nil
	case 2, 3:
		if arg > uint64(len(b)) {
			return nil, nil, errCBOR
		}
		s := b[:arg]
		if major == 3 {
			return string(s), b[arg:], nil
		}
		return append([]byte(nil), s...), b[arg:], nil
	case 4:
		if arg > uint64(len(b)) { // every item takes at least one byte
			return nil, nil, errCBOR
		}
		out := make([]any, 0, arg)
		for range arg {
			v, rest, err := cborItem(b, depth+1)
			if err != nil {
				return nil, nil, err
			}
			out, b = append(out, v), rest
		}
		return out, b, nil
	case 5:
		if arg > uint64(len(b))/2 {
			return nil, nil, errCBOR
		}
		out := make(map[any]any, arg)
		for range arg {
			k, rest, err := cborItem(b, depth+1)
			if err != nil {
				return nil, nil, err
			}
			switch k.(type) {
			case int64, string:
			default:
				return nil, nil, errCBOR
			}
			if _, dup := out[k]; dup {
				return nil, nil, errCBOR
			}
			v, rest, err := cborItem(rest, depth+1)
			if err != nil {
				return nil, nil, err
			}
			out[k], b = v, rest
		}
		return out, b, nil
	case 6:
		return cborItem(b, depth+1)
	default: // 7: false, true, null, undefined, other simple values and floats
		return nil, b, nil
	}
}
