package accountbundle

import (
	"encoding/base64"
	"testing"

	"github.com/bejix/upstream-ops/backend/storage"
)

func TestCredentialEnvelopeRoundTripAndRandomness(t *testing.T) {
	aad := []byte("site-account-bundle-test")
	credentials := map[string]CredentialSecret{
		"account-1": {Mode: storage.CredentialModePassword, Value: "secret-password"},
		"account-2": {Mode: storage.CredentialModeToken, Value: `{"access_token":"token"}`},
	}
	first, err := sealCredentials("export-passphrase", credentials, aad)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealCredentials("export-passphrase", credentials, aad)
	if err != nil {
		t.Fatal(err)
	}
	if first.Salt == second.Salt || first.Nonce == second.Nonce || first.Ciphertext == second.Ciphertext {
		t.Fatal("credential envelopes must use fresh randomness")
	}
	opened, err := openCredentials("export-passphrase", first, aad)
	if err != nil {
		t.Fatal(err)
	}
	if opened["account-1"].Value != "secret-password" || opened["account-2"].Mode != storage.CredentialModeToken {
		t.Fatalf("unexpected credentials: %#v", opened)
	}
}

func TestCredentialEnvelopeRejectsEmptyWrongAndTamperedPassword(t *testing.T) {
	aad := []byte("site-account-bundle-test")
	if _, err := sealCredentials("", map[string]CredentialSecret{}, aad); err == nil {
		t.Fatal("expected empty password error")
	}
	envelope, err := sealCredentials("correct", map[string]CredentialSecret{
		"account-1": {Mode: storage.CredentialModePassword, Value: "secret"},
	}, aad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openCredentials("wrong", envelope, aad); err == nil {
		t.Fatal("expected wrong password error")
	}
	raw, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	envelope.Ciphertext = base64.StdEncoding.EncodeToString(raw)
	if _, err := openCredentials("correct", envelope, aad); err == nil {
		t.Fatal("expected tampered ciphertext error")
	}
	if _, err := openCredentials("correct", envelope, []byte("different-aad")); err == nil {
		t.Fatal("expected authenticated data mismatch")
	}
}
