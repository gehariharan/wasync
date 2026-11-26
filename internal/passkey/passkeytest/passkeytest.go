// Package passkeytest is a software passkey for tests: it makes the same
// registrations and assertions a browser hands back from a real authenticator,
// signed with a key held in memory. Tests use it to play the person at another
// device; it is never part of wasync itself.
package passkeytest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"sort"

	"github.com/gehariharan/wasync/internal/passkey"
)

// Authenticator holds one passkey. Change the fields after New to make it
// misbehave the way a test needs.
type Authenticator struct {
	ID  []byte
	Alg int
	// Origin overrides the page origin put in client data ("" uses https://host).
	Origin string
	// RPID overrides the host whose hash goes in authenticator data.
	RPID string
	// Flags overrides the authenticator data flags (0 uses present and verified).
	Flags byte
	// Counter is the signature counter; it goes up by one per assertion when non-zero.
	Counter uint32
	// Type overrides the client data type ("" uses the right one).
	Type string

	key crypto.Signer
}

// New makes a passkey with a random ID for one of passkey.AlgES256,
// passkey.AlgEdDSA or passkey.AlgRS256.
func New(alg int) *Authenticator {
	a := &Authenticator{ID: make([]byte, 16), Alg: alg}
	_, _ = rand.Read(a.ID)
	var err error
	switch alg {
	case passkey.AlgEdDSA:
		_, a.key, err = ed25519.GenerateKey(rand.Reader)
	case passkey.AlgRS256:
		a.key, err = rsa.GenerateKey(rand.Reader, 2048)
	default:
		a.key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	if err != nil {
		panic(err)
	}
	return a
}

func (a *Authenticator) clientData(kind, host string, challenge []byte) []byte {
	origin := a.Origin
	if origin == "" {
		origin = passkey.Origin(host)
	}
	if a.Type != "" {
		kind = a.Type
	}
	b, _ := json.Marshal(map[string]any{"type": kind, "challenge": base64.RawURLEncoding.EncodeToString(challenge), "origin": origin, "crossOrigin": false})
	return b
}

func (a *Authenticator) authData(host string, attested []byte) []byte {
	rp := host
	if a.RPID != "" {
		rp = a.RPID
	}
	h := sha256.Sum256([]byte(rp))
	flags := a.Flags
	if flags == 0 {
		flags = 0x05 // present and verified
	}
	if attested != nil {
		flags |= 0x40
	}
	out := append(h[:], flags)
	out = binary.BigEndian.AppendUint32(out, a.Counter)
	return append(out, attested...)
}

// Register answers navigator.credentials.create for host and challenge.
func (a *Authenticator) Register(host string, challenge []byte) passkey.Registration {
	cred := make([]byte, 16) // AAGUID: all zero, as with "none" attestation
	cred = binary.BigEndian.AppendUint16(cred, uint16(len(a.ID)))
	cred = append(cred, a.ID...)
	cred = append(cred, a.COSEKey()...)
	obj := encode(map[any]any{"fmt": "none", "attStmt": map[any]any{}, "authData": a.authData(host, cred)})
	return passkey.Registration{ID: append([]byte(nil), a.ID...), ClientDataJSON: a.clientData("webauthn.create", host, challenge), AttestationObject: obj}
}

// Assert answers navigator.credentials.get for host and challenge.
func (a *Authenticator) Assert(host string, challenge []byte) passkey.Assertion {
	if a.Counter != 0 {
		a.Counter++
	}
	client := a.clientData("webauthn.get", host, challenge)
	ad := a.authData(host, nil)
	digest := sha256.Sum256(client)
	signed := append(append([]byte(nil), ad...), digest[:]...)
	var sig []byte
	var err error
	switch k := a.key.(type) {
	case ed25519.PrivateKey:
		sig = ed25519.Sign(k, signed)
	default:
		h := sha256.Sum256(signed)
		sig, err = k.Sign(rand.Reader, h[:], crypto.SHA256)
	}
	if err != nil {
		panic(err)
	}
	return passkey.Assertion{ID: append([]byte(nil), a.ID...), ClientDataJSON: client, AuthenticatorData: ad, Signature: sig}
}

// COSEKey is the public key in COSE form, as in authenticator data.
func (a *Authenticator) COSEKey() []byte {
	switch pub := a.key.Public().(type) {
	case ed25519.PublicKey:
		return encode(map[any]any{int64(1): int64(1), int64(3): int64(passkey.AlgEdDSA), int64(-1): int64(6), int64(-2): []byte(pub)})
	case *rsa.PublicKey:
		return encode(map[any]any{int64(1): int64(3), int64(3): int64(passkey.AlgRS256), int64(-1): pub.N.Bytes(), int64(-2): big.NewInt(int64(pub.E)).Bytes()})
	case *ecdsa.PublicKey:
		raw, _ := pub.Bytes()
		return encode(map[any]any{int64(1): int64(2), int64(3): int64(passkey.AlgES256), int64(-1): int64(1), int64(-2): raw[1:33], int64(-3): raw[33:]})
	}
	panic("unknown key")
}

// encode writes the few CBOR types WebAuthn uses, with map keys in a fixed order.
func encode(v any) []byte {
	head := func(major byte, n uint64) []byte {
		switch {
		case n < 24:
			return []byte{major<<5 | byte(n)}
		case n < 1<<8:
			return []byte{major<<5 | 24, byte(n)}
		case n < 1<<16:
			return binary.BigEndian.AppendUint16([]byte{major<<5 | 25}, uint16(n))
		default:
			return binary.BigEndian.AppendUint32([]byte{major<<5 | 26}, uint32(n))
		}
	}
	switch x := v.(type) {
	case int64:
		if x >= 0 {
			return head(0, uint64(x))
		}
		return head(1, uint64(-1-x))
	case []byte:
		return append(head(2, uint64(len(x))), x...)
	case string:
		return append(head(3, uint64(len(x))), x...)
	case map[any]any:
		keys := make([][]byte, 0, len(x))
		values := map[string][]byte{}
		for k, val := range x {
			ek := encode(k)
			keys = append(keys, ek)
			values[string(ek)] = encode(val)
		}
		sort.Slice(keys, func(i, j int) bool { return string(keys[i]) < string(keys[j]) })
		out := head(5, uint64(len(x)))
		for _, k := range keys {
			out = append(append(out, k...), values[string(k)]...)
		}
		return out
	}
	panic("cannot encode")
}
