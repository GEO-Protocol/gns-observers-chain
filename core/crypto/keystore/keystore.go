package keystore

import (
	e "crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"geo-observers-blockchain/core/common/types/hash"
	"geo-observers-blockchain/core/crypto/ecdsa"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
)

type KeyStore struct {
	pkey *e.PrivateKey
}

func New() (keystore *KeyStore, err error) {
	keyFile, err := os.Open("p521.key")
	if err != nil {
		return
	}

	pemEncoded, err := ioutil.ReadAll(keyFile)
	if err != nil {
		return
	}

	keystore = &KeyStore{}
	err = keystore.decodePKeyFromPem(string(pemEncoded))
	if err != nil {
		return
	}

	return
}

func (k *KeyStore) IsEqualPubKey(key *e.PublicKey) bool {
	return k.pkey.PublicKey.X.Cmp(key.X) == 0 &&
		k.pkey.PublicKey.Y.Cmp(key.Y) == 0
}

func (k *KeyStore) SignHash(h hash.SHA256Container) (signature *ecdsa.Signature, err error) {
	signature = &ecdsa.Signature{}
	signature.R, signature.S, err = e.Sign(rand.Reader, k.pkey, h.Bytes[:])
	return
}

func (k *KeyStore) CheckOwnSignature(h hash.SHA256Container, sig ecdsa.Signature) bool {
	return e.Verify(&k.pkey.PublicKey, h.Bytes[:], sig.R, sig.S)
}

func (k *KeyStore) CheckExternalSignature(h hash.SHA256Container, sig ecdsa.Signature, pubKey *e.PublicKey) bool {
	return e.Verify(pubKey, h.Bytes[:], sig.R, sig.S)
}

func (k *KeyStore) encodePKeyToPem() (pemEncoded string, err error) {
	x509Encoded, err := x509.MarshalECPrivateKey(k.pkey)
	if err != nil {
		return
	}

	pemEncodedBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509Encoded})
	pemEncoded = string(pemEncodedBytes)
	return
}

func (k *KeyStore) encodePubKeyToPem() (pemEncoded string, err error) {
	x509Encoded, err := x509.MarshalPKIXPublicKey(&k.pkey.PublicKey)
	if err != nil {
		return
	}

	pemEncodedBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: x509Encoded})
	pemEncoded = string(pemEncodedBytes)
	return
}

func (k *KeyStore) decodePKeyFromPem(pemEncodedPKey string) (err error) {
	block, _ := pem.Decode([]byte(pemEncodedPKey))
	k.pkey, err = x509.ParseECPrivateKey(block.Bytes)
	return
}

func (k *KeyStore) log() *log.Entry {
	return log.WithFields(log.Fields{"prefix": "keystore"})
}
