package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// embeddedPubKey is the ed25519 public key that signs the catalog.
// Rotating it requires a new glp release. That is the point.
//
// Regenerate with:  go run ./tools/sign keygen
// Override for local development with GLP_TRUST_KEY=<hex>.
var embeddedPubKey = "d720c08a71366c90bb3c330a741c15ff7ecd7314a14d881332b3c32dd5ddf59d"

func trustKey() (ed25519.PublicKey, error) {
	raw := embeddedPubKey
	if env := strings.TrimSpace(os.Getenv("GLP_TRUST_KEY")); env != "" {
		raw = env
	}
	k, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("trust key is not valid hex: %w", err)
	}
	if len(k) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("trust key must be %d bytes, got %d", ed25519.PublicKeySize, len(k))
	}
	return ed25519.PublicKey(k), nil
}

// verifySignature checks a base64-encoded detached signature over payload.
func verifySignature(payload, sigFile []byte) error {
	key, err := trustKey()
	if err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigFile)))
	if err != nil {
		return fmt.Errorf("signature is not valid base64: %w", err)
	}
	if len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("signature must be %d bytes, got %d", ed25519.SignatureSize, len(raw))
	}
	if !ed25519.Verify(key, payload, raw) {
		return errors.New("signature does not match the trusted key, refusing to continue")
	}
	return nil
}
