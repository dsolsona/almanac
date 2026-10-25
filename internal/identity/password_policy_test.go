package identity

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestValidateNewPasswordCommonMembershipAndTextContract(t *testing.T) {
	for _, password := range []string{"passwordpassword", "123456789012345", "qwertyuiopasdfghjkl"} {
		err := ValidateNewPassword(password)
		if !errors.Is(err, ErrCommonPassword) {
			t.Errorf("ValidateNewPassword(%q) = %v, want ErrCommonPassword", password, err)
		}
		if err != nil && strings.Contains(err.Error(), password) {
			t.Errorf("policy error disclosed submitted password")
		}
	}
	for _, password := range []string{
		"a carefully chosen private phrase",
		"QWERTYUIOPASDFGHJKL",
		" qwertyuiopasdfghjkl",
		"qwertyuiopasdfghjkl ",
		strings.Repeat("v", 14),
		strings.Repeat("v", 129),
		strings.Repeat("v", 128),
		strings.Repeat("v", 15),
		"ééééééééééééééé",
		strings.Repeat("e\u0301", 15),
		strings.Repeat("😀", 15),
		strings.Repeat("a", 14) + "\xff",
	} {
		err := ValidateNewPassword(password)
		valid := err == nil
		if password == strings.Repeat("v", 14) || password == strings.Repeat("v", 129) || password == strings.Repeat("a", 14)+"\xff" {
			if valid {
				t.Errorf("ValidateNewPassword(%q) accepted invalid text", password)
			}
			continue
		}
		if !valid {
			t.Errorf("ValidateNewPassword(%q) = %v, want accepted", password, err)
		}
	}
}

func TestPasswordListEmbeddedAssetIntegrity(t *testing.T) {
	data, err := passwordData.ReadFile(passwordListPath)
	if err != nil {
		t.Fatalf("read embedded list: %v", err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != passwordListSHA256 {
		t.Fatalf("embedded list checksum = %s, want pinned checksum", got)
	}
	entries, err := parsePasswordList(data)
	if err != nil {
		t.Fatalf("parse embedded list: %v", err)
	}
	if len(entries) != passwordListEntryCount {
		t.Fatalf("entry count = %d, want %d", len(entries), passwordListEntryCount)
	}
	if _, ok := entries[""]; !ok {
		t.Fatal("pinned source's empty entry was not preserved")
	}
	for _, asset := range []string{"passworddata/LICENSE", "passworddata/README.md"} {
		if _, err := passwordData.ReadFile(asset); err != nil {
			t.Errorf("embedded asset %s: %v", asset, err)
		}
	}
	if _, err := validatePasswordList(append([]byte(nil), data...)); err != nil {
		t.Fatalf("validate pinned dataset: %v", err)
	}
	tampered := append([]byte(nil), data...)
	tampered[0] ^= 1
	if _, err := validatePasswordList(tampered); !errors.Is(err, ErrPasswordPolicyUnavailable) {
		t.Fatalf("tampered checksum error = %v, want ErrPasswordPolicyUnavailable", err)
	}
}

func TestParsePasswordListRejectsMalformedData(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("one\n\xff\n"), []byte("one\r\ntwo\n"), []byte("one\x00bad\n")} {
		if _, err := parsePasswordList(data); err == nil {
			t.Errorf("parsePasswordList(%q) succeeded", data)
		}
	}
}

func TestPasswordPolicyAndHashVerificationRemainSeparate(t *testing.T) {
	const password = "passwordpassword"
	if err := ValidateNewPassword(password); !errors.Is(err, ErrCommonPassword) {
		t.Fatalf("policy error = %v", err)
	}
	hasher := NewPasswordHasher()
	encoded, err := hasher.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := hasher.Verify(t.Context(), password, encoded)
	if err != nil || !ok {
		t.Fatalf("Verify listed password = %t, %v", ok, err)
	}
}

func TestValidateNewPasswordConcurrent(t *testing.T) {
	const callers = 48
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			if err := ValidateNewPassword("a carefully chosen private phrase"); err != nil {
				t.Errorf("ValidateNewPassword() = %v", err)
			}
			if err := ValidateNewPassword("passwordpassword"); !errors.Is(err, ErrCommonPassword) {
				t.Errorf("common password error = %v", err)
			}
		})
	}
	wg.Wait()
}
