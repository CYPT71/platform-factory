package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/mtls"
	"github.com/CYPT71/secure-oci-base/internal/provenance"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "platform-factory-worker:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("platform-factory-worker", flag.ContinueOnError)
	controlPlaneURL := fs.String("control-plane", "", "https://host:port of the control plane (required)")
	certPath := fs.String("cert", "", "worker certificate PEM path, CommonName is this worker's identity "+
		"and Subject Organization must include \"worker\" (required)")
	keyPath := fs.String("key", "", "worker private key PEM path (required)")
	caPath := fs.String("ca", "", "CA bundle PEM path trusted for the control plane's server certificate (required)")
	platform := fs.String("platform", "", "platform to advertise, e.g. linux/amd64 (defaults to GOOS/GOARCH)")
	capabilities := fs.String("capabilities", "", "comma-separated scheduler capabilities")
	cachedContent := fs.String("cached-content", "", "comma-separated sha256 content digests already present locally")
	maxParallel := fs.Int("max-parallel", 1, "maximum number of concurrently assigned leases")
	heartbeatInterval := fs.Duration("heartbeat-interval", 5*time.Second, "")
	pollInterval := fs.Duration("poll-interval", 2*time.Second, "")
	executionDuration := fs.Duration("simulated-execution-duration", 500*time.Millisecond,
		"duration of the built-in demonstration executor")
	signProvenance := fs.Bool("sign-provenance", false, "generate a workload identity at startup and sign every "+
		"lease completion's provenance record with it; the control plane only verifies signatures for workers "+
		"that registered a public key, so this is safe to enable per-worker independently")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *controlPlaneURL == "" || *certPath == "" || *keyPath == "" || *caPath == "" {
		return fmt.Errorf("-control-plane, -cert, -key, and -ca are all required")
	}
	endpoint, err := url.Parse(*controlPlaneURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return fmt.Errorf("-control-plane must be an https://host[:port] URL without credentials, path, query, or fragment")
	}
	if *heartbeatInterval <= 0 || *pollInterval <= 0 || *executionDuration <= 0 {
		return fmt.Errorf("-heartbeat-interval, -poll-interval, and -simulated-execution-duration must be greater than zero")
	}
	if *maxParallel <= 0 || *maxParallel > 1024 {
		return fmt.Errorf("-max-parallel must be between 1 and 1024")
	}
	if *platform == "" {
		*platform = defaultPlatform()
	}

	certificate, err := tls.LoadX509KeyPair(*certPath, *keyPath)
	if err != nil {
		return fmt.Errorf("load worker certificate: %w", err)
	}
	caPEM, err := os.ReadFile(*caPath)
	if err != nil {
		return fmt.Errorf("read CA bundle: %w", err)
	}
	tlsConfig, err := mtls.ClientConfig(mtls.Options{Certificates: []tls.Certificate{certificate}, CAPEM: caPEM})
	if err != nil {
		return fmt.Errorf("build TLS config: %w", err)
	}

	client := &Client{
		HTTP:    &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}, Timeout: 30 * time.Second},
		BaseURL: *controlPlaneURL,
		Execute: func(ctx context.Context, lease Lease) (string, error) {
			return simulateExecutionFor(ctx, lease, *executionDuration)
		},
		MaxParallel: *maxParallel,
	}
	if *signProvenance {
		leaf, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			return fmt.Errorf("parse worker certificate for its CommonName: %w", err)
		}
		if leaf.Subject.CommonName == "" {
			return fmt.Errorf("-sign-provenance requires a worker certificate with a CommonName")
		}
		identity, privateKey, err := provenance.GenerateWorkloadIdentity(leaf.Subject.CommonName)
		if err != nil {
			return fmt.Errorf("generate workload identity: %w", err)
		}
		signer, err := provenance.NewWorkloadSigner(identity, privateKey)
		if err != nil {
			return fmt.Errorf("create workload signer: %w", err)
		}
		client.Signer = signer
		client.WorkerID = leaf.Subject.CommonName
		log.Printf("platform-factory-worker: generated workload identity %s for provenance signing", identity.ID)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("platform-factory-worker: registering with %s as platform %s", *controlPlaneURL, *platform)
	if err := client.RegisterWithOptions(ctx, Registration{
		Platform: *platform, Capabilities: splitCSV(*capabilities),
		CachedContent: splitCSV(*cachedContent), MaxParallel: *maxParallel,
	}); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	log.Printf("platform-factory-worker: registered, polling every %s", *pollInterval)
	return client.Run(ctx, *heartbeatInterval, *pollInterval)
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// simulateExecution stands in for real pipeline stage execution: this
// binary proves distributed coordination (registration, mutual auth,
// lease distribution, idempotent reassignment after worker loss), which
// is orthogonal to what a lease's payload actually means. Wiring a lease
// to a real internal/pipeline run is separate, later work.
func simulateExecution(ctx context.Context, lease Lease) (string, error) {
	return simulateExecutionFor(ctx, lease, 500*time.Millisecond)
}

func simulateExecutionFor(ctx context.Context, lease Lease, duration time.Duration) (string, error) {
	select {
	case <-time.After(duration):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "processed: " + lease.Payload, nil
}
