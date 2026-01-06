package identity

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"

	"gosuda.org/portal/portal/corev2/common"
)

type CertificateV2 struct {
	Version   uint16
	ID        string
	Pubkey    [32]byte
	CreatedAt uint64
	ExpiresAt uint64
	Claims    []byte
	Signature [64]byte
}

func VerifyCert(cert CertificateV2) error {
	if cert.Version != 2 {
		return common.ErrInvalidVersion
	}

	derivedID := DeriveID(cert.Pubkey)
	if derivedID != cert.ID {
		return common.ErrIDMismatch
	}

	canonicalBytes, err := cert.CanonicalBytes()
	if err != nil {
		return err
	}

	if !ed25519.Verify(cert.Pubkey[:], canonicalBytes, cert.Signature[:]) {
		return common.ErrInvalidSignature
	}

	return nil
}

func (c *CertificateV2) CanonicalBytes() ([]byte, error) {
	buf := new(bytes.Buffer)

	if err := binary.Write(buf, binary.BigEndian, c.Version); err != nil {
		return nil, err
	}

	if err := binary.Write(buf, binary.BigEndian, uint16(len(c.ID))); err != nil {
		return nil, err
	}
	if _, err := buf.WriteString(c.ID); err != nil {
		return nil, err
	}

	if _, err := buf.Write(c.Pubkey[:]); err != nil {
		return nil, err
	}

	if err := binary.Write(buf, binary.BigEndian, c.CreatedAt); err != nil {
		return nil, err
	}

	if err := binary.Write(buf, binary.BigEndian, c.ExpiresAt); err != nil {
		return nil, err
	}

	if err := binary.Write(buf, binary.BigEndian, uint16(len(c.Claims))); err != nil {
		return nil, err
	}
	if _, err := buf.Write(c.Claims); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (c *CertificateV2) Sign(cred *Credential) error {
	canonicalBytes, err := c.CanonicalBytes()
	if err != nil {
		return err
	}

	sig := cred.Sign(canonicalBytes)
	copy(c.Signature[:], sig)
	return nil
}

func (c *CertificateV2) Serialize() ([]byte, error) {
	canonicalBytes, err := c.CanonicalBytes()
	if err != nil {
		return nil, err
	}

	result := append(canonicalBytes, c.Signature[:]...)
	return result, nil
}

func DeserializeCertificateV2(data []byte) (*CertificateV2, error) {
	if len(data) < 2 {
		return nil, errors.New("certificate too short")
	}

	cert := &CertificateV2{}
	buf := bytes.NewReader(data)

	if err := binary.Read(buf, binary.BigEndian, &cert.Version); err != nil {
		return nil, err
	}

	var idLen uint16
	if err := binary.Read(buf, binary.BigEndian, &idLen); err != nil {
		return nil, err
	}

	idBytes := make([]byte, idLen)
	if _, err := buf.Read(idBytes); err != nil {
		return nil, err
	}
	cert.ID = string(idBytes)

	if _, err := buf.Read(cert.Pubkey[:]); err != nil {
		return nil, err
	}

	if err := binary.Read(buf, binary.BigEndian, &cert.CreatedAt); err != nil {
		return nil, err
	}

	if err := binary.Read(buf, binary.BigEndian, &cert.ExpiresAt); err != nil {
		return nil, err
	}

	var claimsLen uint16
	if err := binary.Read(buf, binary.BigEndian, &claimsLen); err != nil {
		return nil, err
	}

	cert.Claims = make([]byte, claimsLen)
	if _, err := buf.Read(cert.Claims); err != nil {
		return nil, err
	}

	if _, err := buf.Read(cert.Signature[:]); err != nil {
		return nil, err
	}

	return cert, nil
}

func NewCertificateV2(cred *Credential, expiresAt uint64, claims []byte) (*CertificateV2, error) {
	cert := &CertificateV2{
		Version:   2,
		ID:        cred.ID(),
		Pubkey:    cred.PublicKeyArray(),
		CreatedAt: 0,
		ExpiresAt: expiresAt,
		Claims:    claims,
	}

	if err := cert.Sign(cred); err != nil {
		return nil, err
	}

	return cert, nil
}
