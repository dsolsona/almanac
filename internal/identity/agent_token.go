package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"

	googleuuid "github.com/google/uuid"
)

const agentTokenPrefix = "almanac_agent_v1_"

var ErrInvalidAgentToken = errors.New("invalid agent token")

// AgentToken contains a one-time token and the verifier persisted for it.
type AgentToken struct {
	ID       string   `json:"id"`
	Token    string   `json:"-"`
	Verifier [32]byte `json:"-"`
}

// NewAgentToken creates an independently random lookup ID and 256-bit secret.
func NewAgentToken() (AgentToken, error) {
	id, err := googleuuid.NewRandom()
	if err != nil {
		return AgentToken{}, errors.New("agent token generation failed")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return AgentToken{}, errors.New("agent token generation failed")
	}
	token := agentTokenPrefix + id.String() + "." + base64.RawURLEncoding.EncodeToString(secret[:])
	return AgentToken{ID: id.String(), Token: token, Verifier: sha256.Sum256(secret[:])}, nil
}

// AgentTokenVerifier validates the exact wire format and hashes decoded secret bytes.
func AgentTokenVerifier(token string) (string, [32]byte, error) {
	const suffixLength = 36 + 1 + 43
	if len(token) != len(agentTokenPrefix)+suffixLength || !strings.HasPrefix(token, agentTokenPrefix) {
		return "", [32]byte{}, ErrInvalidAgentToken
	}
	rest := strings.TrimPrefix(token, agentTokenPrefix)
	idText, secretText, ok := strings.Cut(rest, ".")
	if !ok || len(idText) != 36 || len(secretText) != 43 {
		return "", [32]byte{}, ErrInvalidAgentToken
	}
	id, err := googleuuid.Parse(idText)
	if err != nil || id.String() != idText {
		return "", [32]byte{}, ErrInvalidAgentToken
	}
	secret, err := base64.RawURLEncoding.DecodeString(secretText)
	if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != secretText {
		return "", [32]byte{}, ErrInvalidAgentToken
	}
	return idText, sha256.Sum256(secret), nil
}

// VerifyAgentToken checks both the public lookup ID and the secret verifier.
func VerifyAgentToken(token, expectedID string, verifier [32]byte) bool {
	id, candidate, err := AgentTokenVerifier(token)
	if err != nil {
		return false
	}
	idMatches := subtle.ConstantTimeCompare([]byte(id), []byte(expectedID))
	secretMatches := subtle.ConstantTimeCompare(candidate[:], verifier[:])
	return idMatches&secretMatches == 1
}
