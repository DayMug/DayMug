package wechat

import (
	"bytes"
	"strings"
	"testing"
)

func TestAESECBRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef")
	tests := []struct {
		name  string
		plain string
	}{
		{"empty", ""},
		{"shorter than a block", "hi"},
		{"exactly one block", "0123456789abcdef"},
		{"multi block", strings.Repeat("payload", 100)},
		{"binary", string([]byte{0, 1, 2, 255, 254})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ciphertext, err := encryptECB([]byte(tc.plain), key)
			if err != nil {
				t.Fatalf("encryptECB: %v", err)
			}
			if len(ciphertext)%aesKeyLen != 0 {
				t.Errorf("ciphertext is %d bytes, want a whole number of blocks", len(ciphertext))
			}
			// The upload handshake declares this length before the bytes exist.
			if got := CiphertextSize(len(tc.plain)); got != len(ciphertext) {
				t.Errorf("CiphertextSize(%d) = %d, want %d", len(tc.plain), got, len(ciphertext))
			}

			got, err := decryptECB(ciphertext, key)
			if err != nil {
				t.Fatalf("decryptECB: %v", err)
			}
			if string(got) != tc.plain {
				t.Errorf("round trip = %q, want %q", got, tc.plain)
			}
		})
	}
}

// PKCS#7 always appends padding, which is what makes the declared ciphertext
// size predictable even for an exact multiple of the block size.
func TestCiphertextSizeAlwaysGrows(t *testing.T) {
	for _, plainSize := range []int{0, 1, 15, 16, 17, 32} {
		got := CiphertextSize(plainSize)
		if got <= plainSize {
			t.Errorf("CiphertextSize(%d) = %d, want more than the plaintext", plainSize, got)
		}
		if got%aesKeyLen != 0 {
			t.Errorf("CiphertextSize(%d) = %d, want a block multiple", plainSize, got)
		}
	}
}

func TestAESRejectsWrongKeyLength(t *testing.T) {
	if _, err := encryptECB([]byte("data"), []byte("short")); err == nil {
		t.Error("encryptECB accepted a key that is not 16 bytes")
	}
}

func TestDecryptRejectsCorruptInput(t *testing.T) {
	key := []byte("0123456789abcdef")
	tests := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"not a block multiple", bytes.Repeat([]byte{1}, 17)},
		{"garbage padding", bytes.Repeat([]byte{0}, 16)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decryptECB(tc.in, key); err == nil {
				t.Error("decryptECB accepted corrupt input")
			}
		})
	}
}

func TestDecryptWithWrongKeyDoesNotPanic(t *testing.T) {
	ciphertext, err := encryptECB([]byte("secret payload"), []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("encryptECB: %v", err)
	}
	// Padding validation usually rejects it; the point is that it never panics
	// and never returns the wrong plaintext as if it were right.
	if got, err := decryptECB(ciphertext, []byte("fedcba9876543210")); err == nil {
		if string(got) == "secret payload" {
			t.Error("the wrong key produced the right plaintext")
		}
	}
}
