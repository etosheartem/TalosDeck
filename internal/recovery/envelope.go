package recovery

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
)

type keyring struct {
	Active string            `json:"active"`
	Keys   map[string][]byte `json:"keys"`
}

func readKeys(path string) (keyring, error) {
	var k keyring
	b, e := os.ReadFile(path)
	if e == nil {
		e = json.Unmarshal(b, &k)
	}
	return k, e
}
func archiveWriter(w io.Writer, path string) (io.WriteCloser, error) {
	k, e := readKeys(path)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(k.Keys[k.Active])
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	h, _ := json.Marshal(map[string]any{"format": "talosdeck-encrypted-backup-v1", "keyId": k.Active})
	if _, e = w.Write(append(h, '\n')); e != nil {
		return nil, e
	}
	return &sealedWriter{w: w, a: a, aad: h}, nil
}

type sealedWriter struct {
	w   io.Writer
	a   cipher.AEAD
	aad []byte
	seq uint64
}

func (s *sealedWriter) record(p []byte) error {
	nonce := make([]byte, s.a.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return e
	}
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], s.seq)
	s.seq++
	aad := append(append([]byte{}, s.aad...), seq[:]...)
	ct := s.a.Seal(nil, nonce, p, aad)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(ct)))
	for _, b := range [][]byte{size[:], nonce, ct} {
		if _, e := s.w.Write(b); e != nil {
			return e
		}
	}
	return nil
}
func (s *sealedWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		size := min(len(p), 64<<10)
		if e := s.record(p[:size]); e != nil {
			return n, e
		}
		n += size
		p = p[size:]
	}
	return n, nil
}
func (s *sealedWriter) Close() error { return s.record(nil) }
func archiveReader(r io.Reader, path string) (io.Reader, error) {
	br := bufio.NewReader(r)
	h, e := br.ReadSlice('\n')
	if e != nil {
		return nil, errors.New("invalid encrypted backup header")
	}
	h = h[:len(h)-1]
	var meta struct {
		Format string `json:"format"`
		KeyID  string `json:"keyId"`
	}
	if json.Unmarshal(h, &meta) != nil || meta.Format != "talosdeck-encrypted-backup-v1" {
		return nil, errors.New("unsupported encrypted backup")
	}
	k, e := readKeys(path)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(k.Keys[meta.KeyID])
	if e != nil {
		return nil, errors.New("matching backup encryption key unavailable")
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &sealedReader{r: br, a: a, aad: append([]byte{}, h...)}, nil
}

type sealedReader struct {
	r        *bufio.Reader
	a        cipher.AEAD
	aad, buf []byte
	seq      uint64
	done     bool
}

func (s *sealedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(s.buf) > 0 {
		n := copy(p, s.buf)
		s.buf = s.buf[n:]
		return n, nil
	}
	if s.done {
		return 0, io.EOF
	}
	var size [4]byte
	if _, e := io.ReadFull(s.r, size[:]); e != nil {
		return 0, io.ErrUnexpectedEOF
	}
	n := binary.BigEndian.Uint32(size[:])
	if n < uint32(s.a.Overhead()) || n > 64<<10+uint32(s.a.Overhead()) {
		return 0, errors.New("invalid encrypted record")
	}
	nonce := make([]byte, s.a.NonceSize())
	if _, e := io.ReadFull(s.r, nonce); e != nil {
		return 0, e
	}
	ct := make([]byte, n)
	if _, e := io.ReadFull(s.r, ct); e != nil {
		return 0, e
	}
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], s.seq)
	s.seq++
	plain, e := s.a.Open(nil, nonce, ct, append(append([]byte{}, s.aad...), seq[:]...))
	if e != nil {
		return 0, errors.New("backup authentication failed")
	}
	if len(plain) == 0 {
		s.done = true
		if _, e = s.r.ReadByte(); e != io.EOF {
			return 0, errors.New("trailing backup data")
		}
		return 0, io.EOF
	}
	s.buf = plain
	return s.Read(p)
}
