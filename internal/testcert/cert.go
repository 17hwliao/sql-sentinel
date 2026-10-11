// Package testcert creates short-lived credentials only in test TempDirs.
package testcert

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

func Generate(dir string) (ca, serverCert, serverKey, clientCert, clientKey string, err error) {
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return "", "", "", "", "", e
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	raw, e := x509.CreateCertificate(rand.Reader, root, root, pub, key)
	if e != nil {
		return "", "", "", "", "", e
	}
	ca = filepath.Join(dir, "ca.pem")
	if e = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), 0600); e != nil {
		return "", "", "", "", "", e
	}
	issue := func(name string, serial int64, usage x509.ExtKeyUsage) (string, string, error) {
		pub, k, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return "", "", e
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		raw, e := x509.CreateCertificate(rand.Reader, cert, root, pub, key)
		if e != nil {
			return "", "", e
		}
		pk, e := x509.MarshalPKCS8PrivateKey(k)
		if e != nil {
			return "", "", e
		}
		certPath, keyPath := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key")
		if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), 0600); e != nil {
			return "", "", e
		}
		if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0600); e != nil {
			return "", "", e
		}
		return certPath, keyPath, nil
	}
	serverCert, serverKey, e = issue("server", 2, x509.ExtKeyUsageServerAuth)
	if e != nil {
		return "", "", "", "", "", e
	}
	clientCert, clientKey, e = issue("client", 3, x509.ExtKeyUsageClientAuth)
	return ca, serverCert, serverKey, clientCert, clientKey, e
}
