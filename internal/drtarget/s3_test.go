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

func TestReceiptRejectsOverflowAndMalformedChecksum(t *testing.T) {
	for _, r := range []Receipt{{Size: 1 << 62, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {Size: 1, SHA256: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}, {Size: -1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}} {
		if validReceipt(r) {
			t.Fatalf("accepted invalid receipt %+v", r)
		}
	}
}
