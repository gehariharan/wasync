// Package passkey checks WebAuthn passkeys: registering one, and later
// checking that the person confirmed an action with it on their own device
// (Touch ID, Face ID, Windows Hello or a security key with a PIN).
//
// It implements the part of the WebAuthn Level 2 relying-party steps
// (https://www.w3.org/TR/webauthn-2/#sctn-rp-operations) that wasync needs,
// with the standard library only:
//
//   - The relying party is one host name (the tailnet name) served over HTTPS
//     on the default port, so the expected origin is exactly "https://" + host.
//   - Every registration and every assertion must show user presence and user
//     verification: the person unlocked the passkey with a fingerprint, face,
//     device PIN or password, not only touched it.
//   - Registration asks for no attestation ("none"). Any attestation statement
//     a browser sends anyway is ignored, never trusted. What makes a passkey
//     trusted is how wasync lets it be registered (a one-time code from the
//     computer running wasync), not who made the authenticator.
//   - Keys may be ES256 (P-256), EdDSA (Ed25519) or RS256 (RSA, 2048 bits or
//     more), which covers platform authenticators on macOS, iOS, Android and
//     Windows and current security keys.
package passkey

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// COSE algorithm identifiers wasync accepts, in the order a browser should prefer them.
const (
	AlgES256 = -7
	AlgEdDSA = -8
	AlgRS256 = -257
)

// Algorithms is what registration offers the authenticator.
var Algorithms = []int{AlgES256, AlgEdDSA, AlgRS256}

// ChallengeSize is the length of every random challenge, in bytes.
const ChallengeSize = 32

// MaxIDLength is the longest credential ID WebAuthn allows.
const MaxIDLength = 1023

const (
	flagUserPresent  = 0x01
	flagUserVerified = 0x04
	flagAttested     = 0x40
)

// ErrNotVerified is every refusal: the details stay out of what a page shows.
var ErrNotVerified = errors.New("passkey not verified")

// Registration is what the browser returns from navigator.credentials.create.
type Registration struct {
	ID                []byte // rawId
	ClientDataJSON    []byte
	AttestationObject []byte
}

// Assertion is what the browser returns from navigator.credentials.get.
type Assertion struct {
	ID                []byte // rawId
	ClientDataJSON    []byte
	AuthenticatorData []byte
	Signature         []byte
}

// Key is a registered passkey's public half.
type Key struct {
	ID        []byte
	PublicKey []byte // SubjectPublicKeyInfo, DER
	Alg       int
	SignCount uint32
}

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotVerified, fmt.Sprintf(format, a...))
}

// Origin is the only page origin wasync accepts for host.
func Origin(host string) string { return "https://" + host }

// VerifyRegistration checks a new passkey made for host against the challenge
// wasync issued and returns its public key.
func VerifyRegistration(host string, challenge []byte, r Registration) (Key, error) {
	if len(r.ID) == 0 || len(r.ID) > MaxIDLength {
		return Key{}, refuse("credential ID length")
	}
	if err := checkClientData(r.ClientDataJSON, "webauthn.create", challenge, host); err != nil {
		return Key{}, err
	}
	v, rest, err := decodeCBOR(r.AttestationObject)
	if err != nil || len(rest) != 0 {
		return Key{}, refuse("attestation object")
	}
	obj, _ := v.(map[any]any)
	authData, hasData := obj["authData"].([]byte)
	_, hasFormat := obj["fmt"].(string)
	if !hasData || !hasFormat {
		return Key{}, refuse("attestation object fields")
	}
	ad, err := parseAuthData(authData, host)
	if err != nil {
		return Key{}, err
	}
	if ad.flags&flagAttested == 0 {
		return Key{}, refuse("no credential in authenticator data")
	}
	// Attested credential data: AAGUID (16), ID length (2), ID, COSE key.
	cred := ad.rest
	if len(cred) < 18 {
		return Key{}, refuse("credential data")
	}
	n := int(binary.BigEndian.Uint16(cred[16:18]))
	if n == 0 || n > MaxIDLength || len(cred) < 18+n {
		return Key{}, refuse("credential ID")
	}
	id := cred[18 : 18+n]
	if !bytes.Equal(id, r.ID) {
		return Key{}, refuse("credential ID does not match")
	}
	coseValue, _, err := decodeCBOR(cred[18+n:]) // extensions may follow
	if err != nil {
		return Key{}, refuse("public key")
	}
	cose, _ := coseValue.(map[any]any)
	pub, alg, err := coseKey(cose)
	if err != nil {
		return Key{}, err
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return Key{}, refuse("public key")
	}
	return Key{ID: append([]byte(nil), id...), PublicKey: spki, Alg: alg, SignCount: ad.signCount}, nil
}

// VerifyAssertion checks that the person confirmed with key on host, for the
// challenge wasync issued, and returns the authenticator's new signature count.
func VerifyAssertion(host string, challenge []byte, key Key, a Assertion) (uint32, error) {
	if len(a.ID) == 0 || subtle.ConstantTimeCompare(a.ID, key.ID) != 1 {
		return 0, refuse("another passkey")
	}
	if err := checkClientData(a.ClientDataJSON, "webauthn.get", challenge, host); err != nil {
		return 0, err
	}
	ad, err := parseAuthData(a.AuthenticatorData, host)
	if err != nil {
		return 0, err
	}
	digest := sha256.Sum256(a.ClientDataJSON)
	signed := append(append([]byte(nil), a.AuthenticatorData...), digest[:]...)
	pub, err := x509.ParsePKIXPublicKey(key.PublicKey)
	if err != nil {
		return 0, refuse("stored key")
	}
	if !verifySignature(key.Alg, pub, signed, a.Signature) {
		return 0, refuse("signature")
	}
	// A counter that does not move forward suggests a cloned authenticator.
	// Passkeys that sync between devices always report zero.
	if (ad.signCount != 0 || key.SignCount != 0) && ad.signCount <= key.SignCount {
		return 0, refuse("signature counter went backwards")
	}
	return ad.signCount, nil
}

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

func checkClientData(raw []byte, kind string, challenge []byte, host string) error {
	var c clientData
	if err := json.Unmarshal(raw, &c); err != nil {
		return refuse("client data")
	}
	if c.Type != kind {
		return refuse("client data type %q", c.Type)
	}
	got, err := base64.RawURLEncoding.DecodeString(c.Challenge)
	if err != nil || len(challenge) == 0 || subtle.ConstantTimeCompare(got, challenge) != 1 {
		return refuse("challenge")
	}
	if c.Origin != Origin(host) {
		return refuse("origin %q", c.Origin)
	}
	if c.CrossOrigin {
		return refuse("cross-origin request")
	}
	return nil
}

type authData struct {
	flags     byte
	signCount uint32
	rest      []byte // attested credential data and extensions, if any
}

// parseAuthData checks the fixed part of authenticator data: the relying
// party's hash and that the person was present and verified.
func parseAuthData(b []byte, host string) (authData, error) {
	if len(b) < 37 {
		return authData{}, refuse("authenticator data")
	}
	want := sha256.Sum256([]byte(host))
	if subtle.ConstantTimeCompare(b[:32], want[:]) != 1 {
		return authData{}, refuse("relying party")
	}
	ad := authData{flags: b[32], signCount: binary.BigEndian.Uint32(b[33:37]), rest: b[37:]}
	if ad.flags&flagUserPresent == 0 || ad.flags&flagUserVerified == 0 {
		return authData{}, refuse("the person was not verified on the device")
	}
	return ad, nil
}

func intKey(m map[any]any, k int64) (int64, bool) {
	v, ok := m[k].(int64)
	return v, ok
}

func bytesKey(m map[any]any, k int64) []byte {
	v, _ := m[k].([]byte)
	return v
}

// coseKey turns a COSE_Key (RFC 9052, RFC 9053) into a Go public key.
func coseKey(m map[any]any) (crypto.PublicKey, int, error) {
	if m == nil {
		return nil, 0, refuse("public key")
	}
	kty, _ := intKey(m, 1)
	alg, _ := intKey(m, 3)
	crv, _ := intKey(m, -1)
	switch {
	case kty == 2 && alg == AlgES256 && crv == 1: // EC2, P-256
		x, y := bytesKey(m, -2), bytesKey(m, -3)
		if len(x) != 32 || len(y) != 32 {
			return nil, 0, refuse("P-256 key")
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return nil, 0, refuse("P-256 key")
		}
		return pub, AlgES256, nil
	case kty == 1 && alg == AlgEdDSA && crv == 6: // OKP, Ed25519
		x := bytesKey(m, -2)
		if len(x) != ed25519.PublicKeySize {
			return nil, 0, refuse("Ed25519 key")
		}
		return ed25519.PublicKey(append([]byte(nil), x...)), AlgEdDSA, nil
	case kty == 3 && alg == AlgRS256: // RSA
		n, e := bytesKey(m, -1), bytesKey(m, -2)
		modulus := new(big.Int).SetBytes(n)
		exponent := new(big.Int).SetBytes(e)
		if modulus.BitLen() < 2048 || modulus.BitLen() > 8192 || !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > 1<<31-1 || exponent.Bit(0) == 0 {
			return nil, 0, refuse("RSA key")
		}
		return &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}, AlgRS256, nil
	}
	return nil, 0, refuse("unsupported key type %d, algorithm %d", kty, alg)
}

func verifySignature(alg int, pub crypto.PublicKey, signed, sig []byte) bool {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		h := sha256.Sum256(signed)
		return alg == AlgES256 && k.Curve == elliptic.P256() && ecdsa.VerifyASN1(k, h[:], sig)
	case ed25519.PublicKey:
		return alg == AlgEdDSA && ed25519.Verify(k, signed, sig)
	case *rsa.PublicKey:
		h := sha256.Sum256(signed)
		return alg == AlgRS256 && rsa.VerifyPKCS1v15(k, crypto.SHA256, h[:], sig) == nil
	}
	return false
}
