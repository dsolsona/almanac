package identity

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestAgentTokenVerifierFixedVector(t *testing.T) {
	const id = "abcdefab-cdef-4abc-8def-abcdefabcdea"
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	token := agentTokenPrefix + id + "." + secret
	gotID, gotVerifier, err := AgentTokenVerifier(token)
	wantVerifier, _ := hex.DecodeString("66687aadf862bd776c8fc18b8e9f8e20089714856ee233b3902a591d0d5f2925")
	if err != nil || gotID != id || !bytes.Equal(gotVerifier[:], wantVerifier) {
		t.Fatal("fixed agent token verifier vector did not match")
	}
	if !VerifyAgentToken(token, id, gotVerifier) || VerifyAgentToken(token, id+"a", gotVerifier) {
		t.Fatal("agent token verification accepted wrong lookup ID")
	}
	otherSecret := make([]byte, 32)
	otherSecret[0] = 1
	wrongSecretToken := agentTokenPrefix + id + "." + base64.RawURLEncoding.EncodeToString(otherSecret)
	if VerifyAgentToken(wrongSecretToken, id, gotVerifier) {
		t.Fatal("agent token verification accepted wrong secret")
	}
	wrong := gotVerifier
	wrong[0] ^= 1
	if VerifyAgentToken(token, id, wrong) {
		t.Fatal("agent token verification accepted wrong verifier")
	}
}

func TestNewAgentTokenIsIndependentAndJSONSafe(t *testing.T) {
	first, err := NewAgentToken()
	if err != nil {
		t.Fatal("agent token generation failed")
	}
	second, err := NewAgentToken()
	if err != nil {
		t.Fatal("agent token generation failed")
	}
	if first.ID == second.ID || first.Token == second.Token || len(first.Verifier) != 32 {
		t.Fatal("generated agent token fields were not independently generated")
	}
	parsedID, parsedVerifier, err := AgentTokenVerifier(first.Token)
	if err != nil || parsedID != first.ID || parsedVerifier != first.Verifier {
		t.Fatal("generated token did not round-trip")
	}
	encoded, err := json.Marshal(first)
	if err != nil || strings.Contains(string(encoded), first.Token) || strings.Contains(string(encoded), base64.RawURLEncoding.EncodeToString(first.Verifier[:])) {
		t.Fatal("agent token JSON exposed secret material")
	}
	if VerifyAgentToken("almanac_session_v1_"+strings.TrimPrefix(first.Token, agentTokenPrefix), first.ID, first.Verifier) {
		t.Fatal("agent token verification accepted a session role prefix")
	}
}

func TestAgentTokenVerifierRejectsMalformedGrammar(t *testing.T) {
	valid := agentTokenPrefix + "abcdefab-cdef-4abc-8def-abcdefabcdea." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	cases := []string{
		valid + "x",
		"almanac_session_v1_" + strings.TrimPrefix(valid, agentTokenPrefix),
		strings.ToUpper(valid),
		strings.Replace(valid, ".", "..", 1),
		valid + "\n",
		strings.Replace(valid, "abcdefab-cdef-4abc-8def-abcdefabcdea", "ABCDEFAB-CDEF-4ABC-8DEF-ABCDEFABCDEA", 1),
		strings.TrimSuffix(valid, "A") + "=",
		strings.TrimSuffix(valid, "A") + "B",
	}
	for i, candidate := range cases {
		if _, _, err := AgentTokenVerifier(candidate); err == nil {
			t.Fatalf("malformed token case %d was accepted", i)
		} else if err != ErrInvalidAgentToken {
			t.Fatalf("malformed token case %d returned unexpected error class", i)
		}
	}
	if _, _, err := AgentTokenVerifier(valid[:len(agentTokenPrefix)+10]); err != ErrInvalidAgentToken {
		t.Fatal("truncated token returned unexpected error")
	}
}
