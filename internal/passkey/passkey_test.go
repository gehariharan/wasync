package passkey_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/gehariharan/wasync/internal/passkey"
	"github.com/gehariharan/wasync/internal/passkey/passkeytest"
)

const host = "machine.example.ts.net"

func challenge(t *testing.T) []byte {
	t.Helper()
	c := make([]byte, passkey.ChallengeSize)
	if _, err := rand.Read(c); err != nil {
		t.Fatal(err)
	}
	return c
}

func register(t *testing.T, a *passkeytest.Authenticator) passkey.Key {
	t.Helper()
	c := challenge(t)
	key, err := passkey.VerifyRegistration(host, c, a.Register(host, c))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestEachAlgorithmRegistersAndConfirms(t *testing.T) {
	for name, alg := range map[string]int{"ES256": passkey.AlgES256, "EdDSA": passkey.AlgEdDSA, "RS256": passkey.AlgRS256} {
		a := passkeytest.New(alg)
		key := register(t, a)
		if key.Alg != alg || !bytes.Equal(key.ID, a.ID) || len(key.PublicKey) == 0 {
			t.Fatalf("%s: registered %+v", name, key)
		}
		c := challenge(t)
		if _, err := passkey.VerifyAssertion(host, c, key, a.Assert(host, c)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestRegistrationIsRefusedWhenAnythingIsOff(t *testing.T) {
	cases := map[string]func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration){
		"another challenge": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			return host, c, a.Register(host, append([]byte{1}, c[1:]...))
		},
		"another origin": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			a.Origin = "https://evil.example"
			return host, c, a.Register(host, c)
		},
		"plain http origin": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			a.Origin = "http://" + host
			return host, c, a.Register(host, c)
		},
		"another relying party": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			a.RPID = "other.example.ts.net"
			return host, c, a.Register(host, c)
		},
		"made for another host": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			return "other.example.ts.net", c, a.Register(host, c)
		},
		"person not verified": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			a.Flags = 0x01 // present, but no fingerprint, face or PIN
			return host, c, a.Register(host, c)
		},
		"an assertion type": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			a.Type = "webauthn.get"
			return host, c, a.Register(host, c)
		},
		"ID does not match": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			r := a.Register(host, c)
			r.ID = []byte("someone else")
			return host, c, r
		},
		"truncated attestation": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			r := a.Register(host, c)
			r.AttestationObject = r.AttestationObject[:len(r.AttestationObject)-5]
			return host, c, r
		},
		"trailing bytes": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			r := a.Register(host, c)
			r.AttestationObject = append(r.AttestationObject, 0)
			return host, c, r
		},
		"no challenge issued": func(a *passkeytest.Authenticator, c []byte) (string, []byte, passkey.Registration) {
			return host, nil, a.Register(host, nil)
		},
	}
	for name, change := range cases {
		a := passkeytest.New(passkey.AlgES256)
		h, c, r := change(a, challenge(t))
		if _, err := passkey.VerifyRegistration(h, c, r); !errors.Is(err, passkey.ErrNotVerified) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAssertionIsRefusedWhenAnythingIsOff(t *testing.T) {
	cases := map[string]func(a *passkeytest.Authenticator, key *passkey.Key, c []byte) passkey.Assertion{
		"another challenge": func(a *passkeytest.Authenticator, _ *passkey.Key, c []byte) passkey.Assertion {
			return a.Assert(host, append([]byte{^c[0]}, c[1:]...))
		},
		"another origin": func(a *passkeytest.Authenticator, _ *passkey.Key, c []byte) passkey.Assertion {
			a.Origin = "https://" + host + ":8443"
			return a.Assert(host, c)
		},
		"another relying party": func(a *passkeytest.Authenticator, _ *passkey.Key, c []byte) passkey.Assertion {
			a.RPID = "example.ts.net"
			return a.Assert(host, c)
		},
		"person not verified": func(a *passkeytest.Authenticator, _ *passkey.Key, c []byte) passkey.Assertion {
			a.Flags = 0x01
			return a.Assert(host, c)
		},
		"person not present": func(a *passkeytest.Authenticator, _ *passkey.Key, c []byte) passkey.Assertion {
			a.Flags = 0x04
			return a.Assert(host, c)
		},
		"a registration type": func(a *passkeytest.Authenticator, _ *passkey.Key, c []byte) passkey.Assertion {
			a.Type = "webauthn.create"
			return a.Assert(host, c)
		},
		"another passkey": func(_ *passkeytest.Authenticator, _ *passkey.Key, c []byte) passkey.Assertion {
			stranger := passkeytest.New(passkey.AlgES256)
			return stranger.Assert(host, c)
		},
		"another key under the same ID": func(a *passkeytest.Authenticator, _ *passkey.Key, c []byte) passkey.Assertion {
			stranger := passkeytest.New(passkey.AlgES256)
			stranger.ID = a.ID
			return stranger.Assert(host, c)
		},
		"changed after signing": func(a *passkeytest.Authenticator, _ *passkey.Key, c []byte) passkey.Assertion {
			r := a.Assert(host, c)
			r.AuthenticatorData[32] |= 0x08
			return r
		},
		"counter went backwards": func(a *passkeytest.Authenticator, key *passkey.Key, c []byte) passkey.Assertion {
			key.SignCount = 10
			a.Counter = 4
			return a.Assert(host, c)
		},
	}
	for name, change := range cases {
		a := passkeytest.New(passkey.AlgES256)
		key := register(t, a)
		c := challenge(t)
		r := change(a, &key, c)
		if _, err := passkey.VerifyAssertion(host, c, key, r); !errors.Is(err, passkey.ErrNotVerified) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTheCounterMustMoveForwardWhenTheAuthenticatorKeepsOne(t *testing.T) {
	a := passkeytest.New(passkey.AlgES256)
	a.Counter = 1
	key := register(t, a)
	c := challenge(t)
	count, err := passkey.VerifyAssertion(host, c, key, a.Assert(host, c))
	if err != nil || count != 2 {
		t.Fatalf("first: %d %v", count, err)
	}
	key.SignCount = count
	newer := a.Assert(host, c)
	// A copy of the authenticator that is behind does not move it forward.
	a.Counter = 1
	stale := a.Assert(host, c)
	if _, err := passkey.VerifyAssertion(host, c, key, stale); err == nil {
		t.Fatal("a stale counter was accepted")
	}
	if _, err := passkey.VerifyAssertion(host, c, key, newer); err != nil {
		t.Fatalf("a newer counter: %v", err)
	}
}

func TestMalformedAttestationObjectsAreRefused(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":               {},
		"indefinite map":      {0xbf, 0xff},
		"truncated bytes":     {0x45, 1, 2},
		"duplicate key":       {0xa2, 0x01, 0x01, 0x01, 0x02},
		"array key":           {0xa1, 0x80, 0x01},
		"huge array":          {0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"too deep":            bytes.Repeat([]byte{0x81}, 20),
		"reserved additional": {0x1c},
	} {
		if _, err := passkey.VerifyRegistration(host, []byte("c"), passkey.Registration{ID: []byte("x"), ClientDataJSON: []byte(`{"type":"webauthn.create","challenge":"Yw","origin":"https://` + host + `"}`), AttestationObject: b}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
