package control

import (
	"context"
	"crypto/tls"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/piwi3910/nexora/mgmt/internal/pki"
)

const serverCertValidity = 30 * 24 * time.Hour

// TLSConfig is the gRPC server TLS configuration: a server certificate issued in memory from the
// CA at every start, and optional client certificates verified against the CA (Enroll runs
// without one; Connect and GetBlob require one via EngineID).
func TLSConfig(ca *pki.CA, serverNames []string) (*tls.Config, error) {
	cert, err := ca.ServerCertificate(serverNames, serverCertValidity)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    ca.Pool(),
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2"},
	}, nil
}

// EngineID returns the CN of the caller's verified client certificate.
func EngineID(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if ok {
		if info, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			if chains := info.State.VerifiedChains; len(chains) > 0 && len(chains[0]) > 0 && chains[0][0].Subject.CommonName != "" {
				return chains[0][0].Subject.CommonName, nil
			}
		}
	}
	return "", status.Error(codes.Unauthenticated, "client certificate required")
}
