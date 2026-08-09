// Package mtls creates conservative TLS configurations for service clients and servers.
//
// Callers authenticating a peer must not treat tls.Dial (or even a
// subsequent Write) returning without error as proof the peer was
// accepted. TLS 1.3 lets a client finish its own handshake step, and send
// application data, before the server's rejection of a missing or
// untrusted client certificate arrives - the rejection alert only
// surfaces on a later Read. Always perform (or wait for) a Read - or an
// explicit application-level acknowledgement - before trusting that
// MutualTLS actually authenticated the connection. See
// internal/mtls/handshake_test.go for a live capture of this behavior.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
)

// Options contains the trust and identity material for a TLS endpoint.
// CA certificates must be PEM encoded. Certificates are supplied in the form
// returned by tls.LoadX509KeyPair or tls.X509KeyPair.
type Options struct {
	CAPEM        []byte
	Certificates []tls.Certificate
	ServerName   string
	MutualTLS    bool
}

// ClientConfig returns a TLS 1.2+ client configuration. TLS 1.3 remains
// enabled because MaxVersion is deliberately left at its Go default.
func ClientConfig(options Options) (*tls.Config, error) {
	roots, err := certificatePool(options.CAPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      roots,
		Certificates: append([]tls.Certificate(nil), options.Certificates...),
		ServerName:   options.ServerName,
	}, nil
}

// ServerConfig returns a TLS 1.2+ server configuration. When MutualTLS is
// enabled, a non-empty CA bundle and a server certificate are required and
// every client certificate is verified against that bundle.
func ServerConfig(options Options) (*tls.Config, error) {
	if len(options.Certificates) == 0 {
		return nil, errors.New("server TLS requires at least one certificate")
	}
	pool, err := certificatePool(options.CAPEM)
	if err != nil {
		return nil, err
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: append([]tls.Certificate(nil), options.Certificates...)}
	if options.MutualTLS {
		if pool == nil {
			return nil, errors.New("mutual TLS requires at least one CA certificate")
		}
		config.ClientAuth = tls.RequireAndVerifyClientCert
		config.ClientCAs = pool
	}
	return config, nil
}

func certificatePool(pem []byte) (*x509.CertPool, error) {
	if len(pem) == 0 {
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA bundle contains no valid PEM certificates")
	}
	return pool, nil
}
