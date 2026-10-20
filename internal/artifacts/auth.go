package artifacts

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// keyFile holds the store's viewer key. Only the owner can read it, so
// only the owner's processes (and browsers they sign in) can use the
// viewer's API or fetch artifact pages.
const keyFile = ".viewer-key"

// ViewerKey returns the store's viewer key, creating it on first use.
func (s *Store) ViewerKey() (string, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.key != "" {
		return s.key, nil
	}
	k, err := loadOrCreateKey(filepath.Join(s.root, keyFile))
	if err != nil {
		return "", fmt.Errorf("artifacts viewer key: %w", err)
	}
	s.key = k
	return k, nil
}

func validKey(k string) bool {
	if len(k) != 64 {
		return false
	}
	_, err := hex.DecodeString(k)
	return err == nil
}

func loadOrCreateKey(p string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	fresh := hex.EncodeToString(b)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, werr := f.WriteString(fresh)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return "", werr
		}
		return fresh, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	// Another process may be writing the file right now; give it a moment.
	for range 20 {
		if raw, err := os.ReadFile(p); err == nil && validKey(string(raw)) {
			return string(raw), nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	// The file is damaged. Replace it in one step so readers never see a
	// partial key.
	tmp := p + "." + newID()
	if err := os.WriteFile(tmp, []byte(fresh), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return fresh, nil
}

func mac(key, msg string) string {
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
}

// pageKey is the per-artifact secret in its page URLs. Knowing one page's
// URL reveals nothing about other artifacts.
func pageKey(key, id string) string { return mac(key, "page\x00"+id)[:32] }

// pingProof shows that the viewer at baseURL knows the key. It covers the
// address so a process on another port can't relay a real viewer's proof.
func pingProof(key, nonce, baseURL string) string {
	return mac(key, "ping\x00"+nonce+"\x00"+baseURL)
}

func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// signInTTL is how long a sign-in ticket stays valid. Ta-Da opens the
// browser right after making one, so this only needs to cover a slow start.
const signInTTL = 2 * time.Minute

// signInTicket makes a single-use ticket that a viewer swaps for the key.
// It's "<expiry>.<nonce>.<mac>", so any process with the key can make one
// and any viewer for the store can check it.
func signInTicket(key string, now time.Time) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	exp := strconv.FormatInt(now.Add(signInTTL).Unix(), 10)
	nonce := hex.EncodeToString(b)
	return exp + "." + nonce + "." + ticketMAC(key, exp, nonce), nil
}

func ticketMAC(key, exp, nonce string) string {
	return mac(key, "signin\x00"+exp+"\x00"+nonce)[:32]
}

// checkTicket reports whether t is a ticket made with key that hasn't
// expired. It returns the ticket's expiry so the caller can remember it
// as used until then.
func checkTicket(key, t string, now time.Time) (int64, bool) {
	parts := strings.Split(t, ".")
	if len(parts) != 3 || len(parts[1]) != 32 {
		return 0, false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	left := exp - now.Unix()
	if left < 0 || left > int64(signInTTL/time.Second)+5 {
		return 0, false
	}
	return exp, equal(parts[2], ticketMAC(key, parts[0], parts[1]))
}

// SignInURL returns u with a sign-in ticket in its fragment. The viewer
// page swaps the ticket for the key, keeps the key in the browser's
// storage for this origin and removes the ticket from the address bar.
// The ticket works once and only for a couple of minutes, so the browser
// command line and history never hold anything lasting; fragments are
// never sent to a server either. Use it only for links Ta-Da opens
// itself, never for text shown to a model.
func (s *Store) SignInURL(u string) string {
	k, err := s.ViewerKey()
	if err != nil {
		return u
	}
	t, err := signInTicket(k, time.Now())
	if err != nil {
		return u
	}
	return u + "#t=" + t
}
