package crypto

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
)

type RSA struct {
	private *rsa.PrivateKey
	public  *rsa.PublicKey
	bits    int
}

// GenerateKeyPair generates a new key pair
func (r *RSA) GenerateKeyPair(key io.Reader, bits int) (err error) {
	privkey, err := rsa.GenerateKey(key, bits)
	if err != nil {
		return
	}

	r.private = privkey
	r.public = &privkey.PublicKey

	return
}

func (r *RSA) PrivateKeyToBytes() []byte {
	privBytes := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(r.private),
		},
	)

	return privBytes
}

func BytesToPrivateKey(priv []byte) (r *RSA, err error) {
	r = &RSA{}
	block, _ := pem.Decode(priv)
	r.private, err = x509.ParsePKCS1PrivateKey(block.Bytes)

	r.public = &r.private.PublicKey
	r.bits = r.private.Size() //TODO

	return
}

func BytesToPublicKey(pub []byte) (r *RSA, err error) {
	r = &RSA{}

	block, _ := pem.Decode(pub)
	ifc, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return
	}

	var ok bool

	if r.public, ok = ifc.(*rsa.PublicKey); !ok {
		err = errors.New("not ok")
		return
	}

	r.bits = r.public.Size() //TODO

	return
}

func (r *RSA) PublicKeyToBytes() (pubBytes []byte, err error) {
	pubASN1, err := x509.MarshalPKIXPublicKey(r.public)
	if err != nil {
		return
	}

	pubBytes = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PUBLIC KEY",
		Bytes: pubASN1,
	})

	return
}

func (r *RSA) Encrypt(msg []byte) (ciphertext []byte, err error) {
	ciphertext, err = rsa.EncryptOAEP(sha512.New(), rand.Reader, r.public, msg, nil)
	return
}

func (r *RSA) Decrypt(ciphertext []byte) (plaintext []byte, err error) {
	plaintext, err = rsa.DecryptOAEP(sha512.New(), rand.Reader, r.private, ciphertext, nil)
	return
}

func StringToRSAKey(str string, bits int) (r *RSA, err error) {
	r = &RSA{bits: bits}

	err = r.GenerateKeyPair(newReader(str), bits)

	return
}
