package secret_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/benenen/dbgraph/internal/secret"
)

const testKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestSealerRoundTripsASecret(t *testing.T) {
	t.Parallel()

	sealer, err := secret.NewSealer(testKeyHex)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	plaintext := "root:pw@tcp(127.0.0.1:3306)/orders?charset=utf8mb4"

	sealed, err := sealer.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed.KeyID != sealer.KeyID() {
		t.Fatalf("KeyID = %q, want %q", sealed.KeyID, sealer.KeyID())
	}
	if strings.Contains(string(sealed.Ciphertext), "root") || strings.Contains(string(sealed.Ciphertext), "pw") {
		t.Fatal("ciphertext contains plaintext fragments")
	}

	opened, err := sealer.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != plaintext {
		t.Fatalf("Open = %q, want %q", opened, plaintext)
	}
}

func TestSealProducesADistinctCiphertextEachTime(t *testing.T) {
	t.Parallel()

	sealer, err := secret.NewSealer(testKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	first, err := sealer.Seal("same input")
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealer.Seal("same input")
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Ciphertext) == string(second.Ciphertext) {
		t.Fatal("two seals of the same plaintext produced identical ciphertext; the nonce is not random")
	}
}

func TestOpenRejectsTamperedCiphertextAndForeignKeys(t *testing.T) {
	t.Parallel()

	sealer, err := secret.NewSealer(testKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealer.Seal("root:pw@tcp(127.0.0.1:3306)/orders")
	if err != nil {
		t.Fatal(err)
	}

	tampered := secret.Sealed{KeyID: sealed.KeyID, Ciphertext: append([]byte(nil), sealed.Ciphertext...)}
	tampered.Ciphertext[len(tampered.Ciphertext)-1] ^= 0xff
	if _, err := sealer.Open(tampered); !errors.Is(err, secret.ErrUnreadableSecret) {
		t.Fatalf("Open(tampered) error = %v, want ErrUnreadableSecret", err)
	}

	other, err := secret.NewSealer("1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(sealed); !errors.Is(err, secret.ErrUnreadableSecret) {
		t.Fatalf("Open with a different key error = %v, want ErrUnreadableSecret", err)
	}
	if other.KeyID() == sealer.KeyID() {
		t.Fatal("different keys produced the same key id")
	}
}

func TestNewSealerRejectsAnUnusableKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "not hexadecimal", key: strings.Repeat("z", 64)},
		{name: "too short", key: "00010203"},
		{name: "too long", key: testKeyHex + "00"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := secret.NewSealer(test.key); !errors.Is(err, secret.ErrInvalidKey) {
				t.Fatalf("NewSealer(%q) error = %v, want ErrInvalidKey", test.name, err)
			}
		})
	}
}

func TestKeyIDDoesNotRevealTheKey(t *testing.T) {
	t.Parallel()

	sealer, err := secret.NewSealer(testKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	keyID := sealer.KeyID()
	if keyID == "" {
		t.Fatal("KeyID is empty")
	}
	if strings.Contains(testKeyHex, keyID) || strings.Contains(keyID, testKeyHex[:16]) {
		t.Fatalf("key id %q derives visibly from the key material", keyID)
	}
}
