package operations

import (
	"context"
	"talosdeck/internal/backup"
	"talosdeck/internal/certificates"
	"testing"
	"time"
)

type blockedHealthBackups struct{ started, release chan struct{} }

func (f blockedHealthBackups) List(ctx context.Context) ([]*backup.BackupInfo, error) {
	close(f.started)
	select {
	case <-f.release:
	case <-ctx.Done():
	}
	return nil, ctx.Err()
}

type observedHealthCertificates struct{ called chan struct{} }

func (f observedHealthCertificates) Check(context.Context) certificates.Report {
	close(f.called)
	return certificates.Report{CheckedAt: time.Now().UTC(), Certificates: []certificates.Certificate{{ID: "fixture", Status: "healthy"}}}
}
func TestSlowBackupDoesNotDelayCertificateObservation(t *testing.T) {
	started, release, called := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		defer close(done)
		(&HealthCollector{ClusterID: "fixture", Backups: blockedHealthBackups{started, release}, Certificates: observedHealthCertificates{called}}).Collect(ctx)
	}()
	<-started
	defer func() { close(release); <-done }()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("slow backup blocked independent certificate evidence")
	}
}
