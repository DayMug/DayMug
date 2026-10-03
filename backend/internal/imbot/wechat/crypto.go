package wechat

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
)

// AES-128-ECB is what the Weixin CDN mandates for message media. ECB leaks
// block-level structure and would never be chosen for new code — it is
// implemented here only because the gateway will not accept anything else, and
// it is confined to this file so no other code path can reach for it.
//
// Go's standard library deliberately ships no ECB mode, so the block loop is
// written out by hand.

// aesKeyLen is the only key length the protocol uses.
const aesKeyLen = 16

var errBadCiphertext = errors.New("wechat: ciphertext is not a whole number of AES blocks")

// encryptECB encrypts with AES-128-ECB and PKCS#7 padding.
func encryptECB(plaintext, key []byte) ([]byte, error) {
	block, err := newAESBlock(key)
	if err != nil {
		return nil, err
	}
	size := block.BlockSize()
	padded := pkcs7Pad(plaintext, size)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += size {
		block.Encrypt(out[i:i+size], padded[i:i+size])
	}
	return out, nil
}

// decryptECB reverses encryptECB.
func decryptECB(ciphertext, key []byte) ([]byte, error) {
	block, err := newAESBlock(key)
	if err != nil {
		return nil, err
	}
	size := block.BlockSize()
	if len(ciphertext) == 0 || len(ciphertext)%size != 0 {
		return nil, fmt.Errorf("%w (%d bytes)", errBadCiphertext, len(ciphertext))
	}
	out := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += size {
		block.Decrypt(out[i:i+size], ciphertext[i:i+size])
	}
	return pkcs7Unpad(out, size)
}

func newAESBlock(key []byte) (cipher.Block, error) {
	if len(key) != aesKeyLen {
		return nil, fmt.Errorf("wechat: AES key must be %d bytes, got %d", aesKeyLen, len(key))
	}
	return aes.NewCipher(key)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	// PKCS#7 always adds padding, so an exact multiple gains a whole block.
	// That is what makes the padded length predictable, which the upload
	// handshake relies on when it declares the ciphertext size up front.
	n := blockSize - len(data)%blockSize
	return append(append([]byte{}, data...), bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("%w (%d bytes)", errBadCiphertext, len(data))
	}
	n := int(data[len(data)-1])
	if n == 0 || n > blockSize || n > len(data) {
		return nil, errors.New("wechat: invalid PKCS#7 padding")
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, errors.New("wechat: invalid PKCS#7 padding")
		}
	}
	return data[:len(data)-n], nil
}

// CiphertextSize reports how many bytes encryptECB will produce for a payload
// of plainSize. The upload handshake has to declare it before the bytes exist.
func CiphertextSize(plainSize int) int {
	return (plainSize/aesKeyLen + 1) * aesKeyLen
}
