package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/CYPT71/platform-factory/internal/control"
	"github.com/CYPT71/platform-factory/internal/mtls"
	"github.com/CYPT71/platform-factory/internal/observability"
	"github.com/CYPT71/platform-factory/internal/provenance"
	"github.com/CYPT71/platform-factory/internal/quota"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "platform-factory-control-plane:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("platform-factory-control-plane", flag.ContinueOnError)
	listen := fs.String("listen", ":8443", "address to listen on")
	certPath := fs.String("cert", "", "server certificate PEM path (required)")
	keyPath := fs.String("key", "", "server private key PEM path (required)")
	caPath := fs.String("ca", "", "CA bundle PEM path trusted for client certificates; only certificates whose "+
		"Subject Organization includes \"worker\" are accepted as workers (required)")
	heartbeatTimeout := fs.Duration("heartbeat-timeout", 30*time.Second, "how long a worker may go without a heartbeat before its lease is reassigned")
	reapInterval := fs.Duration("reap-interval", 5*time.Second, "how often to check for expired worker heartbeats")
	statePath := fs.String("state-file", "", "durable scheduler snapshot path (required for restart recovery)")
	auditPath := fs.String("audit-file", "", "append-only hash-chained lifecycle journal path")
	tenantMaxParallel := fs.Int("tenant-max-parallel", 0, "if greater than zero, cap concurrent leases per tenant "+
		"(POST /lease/submit's optional \"tenant\" field); leases that omit a tenant are never limited; "+
		"quota state is in-memory only and does not survive a restart")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *certPath == "" || *keyPath == "" || *caPath == "" {
		return fmt.Errorf("-cert, -key, and -ca are all required")
	}
	if *listen == "" {
		return fmt.Errorf("-listen must not be empty")
	}
	if *heartbeatTimeout <= 0 || *reapInterval <= 0 {
		return fmt.Errorf("-heartbeat-timeout and -reap-interval must be greater than zero")
	}

	certificate, err := tls.LoadX509KeyPair(*certPath, *keyPath)
	if err != nil {
		return fmt.Errorf("load server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(*caPath)
	if err != nil {
		return fmt.Errorf("read CA bundle: %w", err)
	}
	tlsConfig, err := mtls.ServerConfig(mtls.Options{
		Certificates: []tls.Certificate{certificate}, CAPEM: caPEM, MutualTLS: true,
	})
	if err != nil {
		return fmt.Errorf("build TLS config: %w", err)
	}

	plane := control.NewControlPlane(*heartbeatTimeout)
	if *statePath != "" {
		loaded, err := control.LoadControlPlane(*heartbeatTimeout, *statePath)
		if err == nil {
			plane = loaded
			observability.Info("restored scheduler state", observability.Fields{"path": *statePath})
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restore scheduler state: %w", err)
		}
		if err := plane.Save(*statePath); err != nil {
			return fmt.Errorf("initialize scheduler state: %w", err)
		}
	}
	var audit *control.AuditJournal
	if *auditPath != "" {
		audit, err = control.OpenAuditJournal(*auditPath)
		if err != nil {
			return fmt.Errorf("open audit journal: %w", err)
		}
		observability.Info("opened audit journal", observability.Fields{"path": *auditPath})
	}
	stop := make(chan struct{})
	go runPersistentReapLoop(plane, *reapInterval, *statePath, audit, stop)
	defer close(stop)

	server := &Server{Plane: plane, StatePath: *statePath, Audit: audit, Provenance: provenance.NewProvenanceStore()}
	if *tenantMaxParallel > 0 {
		server.Scheduler = quota.NewFairScheduler(quota.NewTenantQuota(quota.Quota{MaxParallel: *tenantMaxParallel}))
		observability.Info("tenant quota enforcement enabled", observability.Fields{"max_parallel": *tenantMaxParallel})
	}
	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           server.Routes(),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	observability.Info("platform-factory-control-plane: listening (mutual TLS required)", observability.Fields{"listen": *listen})
	if err := httpServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
