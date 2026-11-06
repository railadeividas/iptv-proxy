// Package proxy implements the header-fixing HLS/DASH/TS proxy.
package proxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// Payload is what a signed proxy URL carries.
type Payload struct {
	U string `json:"u"`           // upstream URL (a directory when Dir is set)
	R string `json:"r,omitempty"` // Referer to send upstream
	A string `json:"a,omitempty"` // User-Agent to send upstream
	E int64  `json:"e,omitempty"` // expiry, unix seconds
	D bool   `json:"d,omitempty"` // U is a directory (DASH templates)
}

type Signer struct{ key []byte }

// LoadKey reads the HMAC key from path, creating it on first start.
func LoadKey(path string) (*Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 32 {
		b = make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, err
		}
	}
	return &Signer{key: b}, nil
}

func NewSigner(key []byte) *Signer { return &Signer{key: key} }

func (s *Signer) mac(data string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(data))
	return m.Sum(nil)[:16]
}

// Encode returns "<payload>.<sig>" in URL-safe base64.
func (s *Signer) Encode(p Payload) string {
	j, _ := json.Marshal(p)
	body := base64.RawURLEncoding.EncodeToString(j)
	return body + "." + base64.RawURLEncoding.EncodeToString(s.mac(body))
}

var ErrBadToken = errors.New("invalid or expired link")

func (s *Signer) Decode(tok string) (Payload, error) {
	var p Payload
	i := strings.LastIndexByte(tok, '.')
	if i < 0 {
		return p, ErrBadToken
	}
	body := tok[:i]
	sig, err := base64.RawURLEncoding.DecodeString(tok[i+1:])
	if err != nil || !hmac.Equal(sig, s.mac(body)) {
		return p, ErrBadToken
	}
	j, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || json.Unmarshal(j, &p) != nil {
		return p, ErrBadToken
	}
	if p.E != 0 && time.Now().Unix() > p.E {
		return p, ErrBadToken
	}
	return p, nil
}
