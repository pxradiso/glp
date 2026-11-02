package main

// Offline catalog signing. Kept out of the client on purpose: the released
// glp binary only ever holds the public key.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keygen":
		keygen(arg(2, "dev.key"))
	case "pub":
		pub(arg(2, "dev.key"))
	case "sign":
		if len(os.Args) < 4 {
			usage()
		}
		sign(arg(2, "dev.key"), os.Args[3])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  sign keygen [privkey-file]     generate a keypair, print the public key (hex)
  sign pub <privkey-file>        print the public key for a private key
  sign sign <privkey-file> <file>  write <file>.sig (base64, detached)
`)
	os.Exit(2)
}

func arg(i int, def string) string {
	if len(os.Args) > i {
		return os.Args[i]
	}
	return def
}

func keygen(path string) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv)+"\n"), 0o600); err != nil {
		fail(err)
	}
	fmt.Printf("private key: %s  (chmod 600, never commit this)\n", path)
	fmt.Printf("\npublic key (embed in trust.go as embeddedPubKey):\n%s\n", hex.EncodeToString(pub))
}

func pub(path string) {
	fmt.Println(hex.EncodeToString(loadPriv(path).Public().(ed25519.PublicKey)))
}

func sign(keyPath, file string) {
	priv := loadPriv(keyPath)
	payload, err := os.ReadFile(file)
	if err != nil {
		fail(err)
	}
	sig := ed25519.Sign(priv, payload)
	out := file + ".sig"
	if err := os.WriteFile(out, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("signed %s -> %s\n", file, out)
}

func loadPriv(path string) ed25519.PrivateKey {
	raw, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	b, err := hex.DecodeString(trimNL(string(raw)))
	if err != nil {
		fail(err)
	}
	if len(b) != ed25519.PrivateKeySize {
		fail(fmt.Errorf("private key must be %d bytes, got %d", ed25519.PrivateKeySize, len(b)))
	}
	return ed25519.PrivateKey(b)
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "sign: %v\n", err)
	os.Exit(1)
}
