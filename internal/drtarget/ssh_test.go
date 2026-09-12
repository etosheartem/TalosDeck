package drtarget

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSSHValidation(t *testing.T) {
	for _, v := range []struct{ host, dir string }{{"-oProxyCommand=bad", "/backup"}, {"root@host;id", "/backup"}, {"host", "/backup';id"}, {"host", "/a/../b"}, {"host", "/"}, {"host", "relative"}, {"host", "/a\nb"}} {
		if validateSSH(v.host, v.dir) == nil {
			t.Fatalf("accepted unsafe target %#v", v)
		}
	}
	for _, host := range []string{"pve", "root@10.42.0.45", "backup.example.org"} {
		if e := validateSSH(host, "/var/backups/talosdeck"); e != nil {
			t.Fatal(e)
		}
	}
}
func TestSSHDownloadRejectsUnsafeReceiptBeforeCreatingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "archive")
	e := SSHDownload(context.Background(), "pve", "/backup", Receipt{Object: "../secret", SHA256: string(bytes.Repeat([]byte{'a'}, 64)), Size: 1}, p)
	if e == nil {
		t.Fatal("accepted traversal")
	}
	if _, e = os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("created local file for invalid receipt")
	}
}
func TestSSHBoundedReadback(t *testing.T) {
	var b bytes.Buffer
	w := sshBoundedWriter{writer: &b, remaining: 3}
	if _, e := w.Write([]byte("abc")); e != nil {
		t.Fatal(e)
	}
	if _, e := w.Write([]byte("x")); e == nil {
		t.Fatal("accepted oversized readback")
	}
	if b.String() != "abc" {
		t.Fatal("unexpected bytes")
	}
}
