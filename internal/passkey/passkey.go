// Package passkey checks WebAuthn passkeys: registering one, and later
// checking that the person confirmed an action with it on their own device
// (Touch ID, Face ID, Windows Hello or a security key with a PIN).
//
// Parsing and verification follow the WebAuthn relying-party steps
// (https://www.w3.org/TR/webauthn-2/#sctn-rp-operations) through the
// maintained github.com/go-webauthn/webauthn protocol package. This package
// fixes the policy wasync needs on top of it:
//
//   - The relying party is one host name (the tailnet name) served over HTTPS
//     on the default port, so the only accepted origin is "https://" + host,
//     and a request made from inside another site's frame is refused.
//   - Every registration and every assertion must show user presence and user
//     verification: the person unlocked the passkey with a fingerprint, face,
//     device PIN or password, not only touched it.
//   - Registration asks for no attestation ("none") and no attestation is
//     trusted: no metadata service is consulted. What makes a passkey trusted
//     is how wasync lets it be registered (a one-time code from the computer
//     running wasync), not who made the authenticator.
//   - Keys may be ES256 (P-256), EdDSA (Ed25519) or RS256 (RSA, 2048 to 8192
//     bits), which covers platform authenticators on macOS, iOS, Android and
//     Windows and current security keys.
//   - A signature counter that does not move forward is refused.
package passkey

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

// COSE algorithm identifiers wasync accepts, in the order a browser should prefer them.
const (
	AlgES256 = int(webauthncose.AlgES256)
	AlgEdDSA = int(webauthncose.AlgEdDSA)
	AlgRS256 = int(webauthncose.AlgRS256)
)

// Algorithms is what registration offers the authenticator.
var Algorithms = []int{AlgES256, AlgEdDSA, AlgRS256}

// ChallengeSize is the length of every random challenge, in bytes.
const ChallengeSize = 32

// MaxIDLength is the longest credential ID WebAuthn allows.
const MaxIDLength = 1023

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
	PublicKey []byte // COSE_Key, as in the authenticator data
	Alg       int
	SignCount uint32
}

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotVerified, fmt.Sprintf(format, a...))
}

// Origin is the only page origin wasync accepts for host.
func Origin(host string) string { return "https://" + host }

func credentialParameters() []protocol.CredentialParameter {
	params := make([]protocol.CredentialParameter, 0, len(Algorithms))
	for _, alg := range Algorithms {
		params = append(params, protocol.CredentialParameter{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.COSEAlgorithmIdentifier(alg)})
	}
	return params
}

func publicKeyCredential(id []byte) protocol.PublicKeyCredential {
	return protocol.PublicKeyCredential{
		Credential: protocol.Credential{ID: base64.RawURLEncoding.EncodeToString(id), Type: string(protocol.PublicKeyCredentialType)},
		RawID:      id,
	}
}

// VerifyRegistration checks a new passkey made for host against the challenge
// wasync issued and returns its public key.
func VerifyRegistration(host string, challenge []byte, r Registration) (Key, error) {
	if len(r.ID) == 0 || len(r.ID) > MaxIDLength {
		return Key{}, refuse("credential ID length")
	}
	if len(challenge) == 0 {
		return Key{}, refuse("challenge")
	}
	response := protocol.CredentialCreationResponse{
		PublicKeyCredential: publicKeyCredential(r.ID),
		AttestationResponse: protocol.AuthenticatorAttestationResponse{
			AuthenticatorResponse: protocol.AuthenticatorResponse{ClientDataJSON: r.ClientDataJSON},
			AttestationObject:     r.AttestationObject,
		},
	}
	parsed, err := response.Parse()
	if err != nil {
		return Key{}, refuse("registration: %v", err)
	}
	if _, err := parsed.Verify(base64.RawURLEncoding.EncodeToString(challenge), host, []string{Origin(host)}, nil, nil, protocol.TopOriginExplicitVerificationMode, false, true, true, nil, credentialParameters(), protocol.AttestationPolicy{}, protocol.SignaturePolicy{}); err != nil {
		return Key{}, refuse("registration: %v", err)
	}
	data := parsed.Response.AttestationObject.AuthData
	cose := data.AttData.CredentialPublicKey
	pub, err := webauthncose.ParsePublicKey(cose)
	if err != nil {
		return Key{}, refuse("public key: %v", err)
	}
	var alg int64
	switch k := pub.(type) {
	case webauthncose.EC2PublicKeyData:
		alg = k.Algorithm
	case webauthncose.OKPPublicKeyData:
		alg = k.Algorithm
	case webauthncose.RSAPublicKeyData:
		if bits := new(big.Int).SetBytes(k.Modulus).BitLen(); bits < 2048 || bits > 8192 {
			return Key{}, refuse("RSA key of %d bits", bits)
		}
		alg = k.Algorithm
	default:
		return Key{}, refuse("unsupported key type")
	}
	return Key{ID: append([]byte(nil), data.AttData.CredentialID...), PublicKey: append([]byte(nil), cose...), Alg: int(alg), SignCount: data.Counter}, nil
}

// VerifyAssertion checks that the person confirmed with key on host, for the
// challenge wasync issued, and returns the authenticator's new signature count.
func VerifyAssertion(host string, challenge []byte, key Key, a Assertion) (uint32, error) {
	if len(a.ID) == 0 || subtle.ConstantTimeCompare(a.ID, key.ID) != 1 {
		return 0, refuse("another passkey")
	}
	if len(challenge) == 0 {
		return 0, refuse("challenge")
	}
	response := protocol.CredentialAssertionResponse{
		PublicKeyCredential: publicKeyCredential(a.ID),
		AssertionResponse: protocol.AuthenticatorAssertionResponse{
			AuthenticatorResponse: protocol.AuthenticatorResponse{ClientDataJSON: a.ClientDataJSON},
			AuthenticatorData:     a.AuthenticatorData,
			Signature:             a.Signature,
		},
	}
	parsed, err := response.Parse()
	if err != nil {
		return 0, refuse("assertion: %v", err)
	}
	if err := parsed.Verify(base64.RawURLEncoding.EncodeToString(challenge), host, "", []string{Origin(host)}, nil, nil, protocol.TopOriginExplicitVerificationMode, false, true, true, key.PublicKey, protocol.SignaturePolicy{}); err != nil {
		return 0, refuse("assertion: %v", err)
	}
	// A counter that does not move forward suggests a cloned authenticator.
	// Passkeys that sync between devices always report zero.
	count := parsed.Response.AuthenticatorData.Counter
	if (count != 0 || key.SignCount != 0) && count <= key.SignCount {
		return 0, refuse("signature counter went backwards")
	}
	return count, nil
}
