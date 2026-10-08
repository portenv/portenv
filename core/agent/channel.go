// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ChannelSecrets are one start's agent channel credentials (ADR 0010): the
// driver writes Token, CertPEM and KeyPEM into the box before it starts and
// keeps Token and CertPEM in memory to connect.
type ChannelSecrets struct {
	Token   string
	CertPEM []byte
	KeyPEM  []byte
}

// NewChannelSecrets makes a 256-bit token and a fresh P-256 key with a
// self-signed certificate. Every start makes new ones; the 30-day validity
// only bounds a box left running that long.
func NewChannelSecrets() (ChannelSecrets, error) {
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return ChannelSecrets{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return ChannelSecrets{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return ChannelSecrets{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "portenv-agent"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"portenv-agent"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return ChannelSecrets{}, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return ChannelSecrets{}, err
	}
	return ChannelSecrets{
		Token:   hex.EncodeToString(tok),
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}),
	}, nil
}

// DialChannel connects to an agent channel: TLS that accepts only the
// pinned certificate, and the token on every call.
func DialChannel(dial func(ctx context.Context) (net.Conn, error), certPEM []byte, token string) (*grpc.ClientConn, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("agent channel: bad pinned certificate")
	}
	pinned := block.Bytes
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: "portenv-agent",
		// The certificate is pinned, not chained to a CA: accept exactly it.
		InsecureSkipVerify: true, // #nosec G402 -- verified by VerifyPeerCertificate below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || !bytes.Equal(raw[0], pinned) {
				return errors.New("agent channel: the certificate is not this box's")
			}
			return nil
		},
	}
	return grpc.NewClient("passthrough:///portenv-agent",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return dial(ctx) }),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithPerRPCCredentials(tokenCreds(token)),
	)
}

type tokenCreds string

func (t tokenCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{TokenHeader: string(t)}, nil
}
func (t tokenCreds) RequireTransportSecurity() bool { return true }
