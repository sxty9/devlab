// Package links stores the per-user GitHub association: a DevLab-owned mapping from a canonical
// Linux username to that user's GitHub OAuth grant. This is NOT a parallel identity store —
// identity remains the Linux account; this only records "this Linux user linked this GitHub
// account, here is their encrypted token". DevLab is the sole consumer, so DevLab owns it.
//
// The OAuth token is a secret: encrypted at rest with AES-256-GCM, never written in cleartext,
// never returned to the browser, never logged.
package links

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// userRe bounds a username to safe filename characters (Linux account names). Defends the
// per-user file path against traversal even though the username comes from a verified JWT.
var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// Link is the public (token-free) view of a user's GitHub association.
type Link struct {
	GHLogin  string `json:"ghLogin"`
	GHID     int64  `json:"ghId"`
	Scopes   string `json:"scopes"`
	LinkedAt string `json:"linkedAt"`
}

// stored is the on-disk shape: Link plus the encrypted token.
type stored struct {
	Link
	TokenEnc string `json:"tokenEnc"` // base64(nonce || ciphertext)
}

// Store persists per-user links under a directory, encrypting tokens with a fixed key.
type Store struct {
	dir string
	gcm cipher.AEAD
}

// NewStore builds the store from the environment: DEVLAB_LINKS (dir, default /var/lib/devlab/links)
// and DEVLAB_LINK_ENC_KEY_FILE (a 32-byte key, raw or hex/base64-encoded). The directory is
// created 0700 if missing.
func NewStore() (*Store, error) {
	dir := os.Getenv("DEVLAB_LINKS")
	if dir == "" {
		dir = "/var/lib/devlab/links"
	}
	key, err := loadKey(os.Getenv("DEVLAB_LINK_ENC_KEY_FILE"))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("links: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("links: gcm: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("links: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir, gcm: gcm}, nil
}

// loadKey reads a 32-byte AES key. The file may hold the raw 32 bytes, or a 64-char hex string,
// or base64 (std/url) — whichever decodes to exactly 32 bytes.
func loadKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("links: DEVLAB_LINK_ENC_KEY_FILE not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("links: read key: %w", err)
	}
	if len(b) == 32 {
		return b, nil
	}
	s := strings.TrimSpace(string(b))
	if len(s) == 32 {
		return []byte(s), nil
	}
	if dec, err := hex.DecodeString(s); err == nil && len(dec) == 32 {
		return dec, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if dec, err := enc.DecodeString(s); err == nil && len(dec) == 32 {
			return dec, nil
		}
	}
	return nil, errors.New("links: DEVLAB_LINK_ENC_KEY_FILE must decode to exactly 32 bytes")
}

func (s *Store) path(user string) (string, error) {
	if !userRe.MatchString(user) {
		return "", fmt.Errorf("links: invalid username %q", user)
	}
	return filepath.Join(s.dir, user+".json"), nil
}

func (s *Store) encrypt(plaintext string) (string, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := s.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func (s *Store) decrypt(enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	ns := s.gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("links: ciphertext too short")
	}
	pt, err := s.gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func (s *Store) read(user string) (*stored, error) {
	p, err := s.path(user)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err // includes os.IsNotExist
	}
	var st stored
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("links: parse %s: %w", p, err)
	}
	return &st, nil
}

// Save (over)writes the user's link, encrypting the token. now is injected so callers control
// the clock (and tests stay deterministic).
func (s *Store) Save(user, ghLogin string, ghID int64, token, scopes string, now time.Time) error {
	p, err := s.path(user)
	if err != nil {
		return err
	}
	enc, err := s.encrypt(token)
	if err != nil {
		return err
	}
	st := stored{
		Link:     Link{GHLogin: ghLogin, GHID: ghID, Scopes: scopes, LinkedAt: now.UTC().Format(time.RFC3339)},
		TokenEnc: enc,
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	// Atomic replace, 0600. tmp in the same dir so os.Rename stays on one filesystem.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Get returns the token-free link metadata, or (nil, nil) when the user has none linked.
func (s *Store) Get(user string) (*Link, error) {
	st, err := s.read(user)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	l := st.Link
	return &l, nil
}

// Linked reports whether the user has a stored link (cheap existence check).
func (s *Store) Linked(user string) bool {
	l, err := s.Get(user)
	return err == nil && l != nil
}

// Token returns the decrypted OAuth token for backend GitHub calls. Never expose this to the
// client. Returns os.ErrNotExist (wrapped) when the user has no link.
func (s *Store) Token(user string) (string, error) {
	st, err := s.read(user)
	if err != nil {
		return "", err
	}
	return s.decrypt(st.TokenEnc)
}

// Delete removes the user's link (idempotent).
func (s *Store) Delete(user string) error {
	p, err := s.path(user)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
