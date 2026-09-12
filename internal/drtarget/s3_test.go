package drtarget

import "testing"

func TestRecoveryTargetRequiresIndependentTLSConfiguration(t *testing.T) {
	for _, endpoint := range []string{"http://backup.example", "https://user:pass@backup.example", "https://backup.example/path", "https://backup.example?token=secret", "file:///backup", ""} {
		_, err := client(Config{Endpoint: endpoint, Bucket: "backup", AccessKey: "id", SecretKey: "secret"})
		if err == nil {
			t.Fatalf("accepted unsafe target %q", endpoint)
		}
	}
	if _, err := client(Config{Endpoint: "https://backup.example", Bucket: "backup", AccessKey: "id", SecretKey: "secret"}); err != nil {
		t.Fatal(err)
	}
}
