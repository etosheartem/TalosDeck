// Package drtarget transfers management recovery archives to independent storage.
package drtarget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Config must be supplied independently of the database being recovered.
// The operator must place this target outside the management host/failure domain.
type Config struct {
	Type      string `json:"type"`
	Host      string `json:"host"`
	Directory string `json:"directory"`
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

func client(c Config) (*minio.Client, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || c.Bucket == "" || c.AccessKey == "" || c.SecretKey == "" {
		return nil, errors.New("recovery target requires HTTPS endpoint, bucket and independent credentials")
	}
	return minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""), Secure: true, Region: c.Region})
}

type Receipt struct {
	Object string `json:"object"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Upload uses a fresh object identity and verifies the uploaded bytes by reading
// them back. A receipt is returned only after successful verification.
func Upload(ctx context.Context, config Config, path string) (Receipt, error) {
	c, err := client(config)
	if err != nil {
		return Receipt{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Receipt{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Receipt{}, err
	}
	r := Receipt{Object: "management/" + uuid.NewString() + ".tdr", SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return Receipt{}, err
	}
	if _, err = c.PutObject(ctx, config.Bucket, r.Object, f, n, minio.PutObjectOptions{ContentType: "application/octet-stream"}); err != nil {
		return r, errors.New("recovery upload outcome unknown; retain local archive and inspect target")
	}
	obj, err := c.GetObject(ctx, config.Bucket, r.Object, minio.GetObjectOptions{})
	if err != nil {
		return r, errors.New("recovery upload verification unavailable")
	}
	defer obj.Close()
	h.Reset()
	got, err := io.Copy(h, io.LimitReader(obj, n+1))
	if err != nil || got != n || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return r, errors.New("recovery upload checksum verification failed")
	}
	return r, nil
}

// Download never overwrites an existing file. The expected checksum comes from
// the independently retained receipt, not mutable object metadata.
func Download(ctx context.Context, config Config, r Receipt, path string) error {
	if r.Size <= 0 || r.Object == "" || len(r.SHA256) != 64 {
		return errors.New("invalid recovery receipt")
	}
	c, err := client(config)
	if err != nil {
		return err
	}
	obj, err := c.GetObject(ctx, config.Bucket, r.Object, minio.GetObjectOptions{})
	if err != nil {
		return errors.New("recovery object unavailable")
	}
	defer obj.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(obj, r.Size+1))
	if err != nil || n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return errors.New("recovery download integrity failure")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
