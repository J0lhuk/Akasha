package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"akasha/internal/core"
	"golang.org/x/crypto/argon2"
)

type Key [32]byte
type wrapped struct {
	Version    int    `json:"version"`
	Salt       []byte `json:"salt"`
	Ciphertext []byte `json:"ciphertext"`
}
type Vault struct {
	root   string
	master Key
	mu     sync.Mutex
}

func random(n int) ([]byte, error) { b := make([]byte, n); _, err := rand.Read(b); return b, err }
func Seal(key Key, ad string, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, err := random(gcm.NonceSize())
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, []byte(ad)), nil
}
func Open(key Key, ad string, sealed []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("invalid encrypted payload")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(ad))
	if err != nil {
		return nil, errors.New("wrong key or modified encrypted payload")
	}
	return plain, nil
}
func Wrap(passphrase []byte, ad string, plain []byte) ([]byte, error) {
	if len(passphrase) < 12 || len(passphrase) > 4096 {
		return nil, errors.New("passphrase must contain 12..4096 bytes")
	}
	salt, err := random(16)
	if err != nil {
		return nil, err
	}
	derived := argon2.IDKey(passphrase, salt, 3, 64*1024, 2, 32)
	defer clear(derived)
	var key Key
	copy(key[:], derived)
	defer clear(key[:])
	sealed, err := Seal(key, ad, plain)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wrapped{1, salt, sealed})
}
func Unwrap(passphrase []byte, ad string, data []byte) ([]byte, error) {
	if len(data) > 1<<20 || len(passphrase) < 12 || len(passphrase) > 4096 {
		return nil, errors.New("invalid key file or passphrase length")
	}
	var w wrapped
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	if w.Version != 1 || len(w.Salt) != 16 {
		return nil, errors.New("unsupported key file")
	}
	derived := argon2.IDKey(passphrase, w.Salt, 3, 64*1024, 2, 32)
	defer clear(derived)
	var key Key
	copy(key[:], derived)
	defer clear(key[:])
	return Open(key, ad, w.Ciphertext)
}
func Unlock(root string, passphrase []byte) (*Vault, error) {
	file := filepath.Join(root, "vault", "master.akasha-key")
	data, err := os.ReadFile(file)
	var raw []byte
	if os.IsNotExist(err) {
		raw, err = random(32)
		if err != nil {
			return nil, err
		}
		data, err = Wrap(passphrase, "akasha/master/v1", raw)
		if err != nil {
			clear(raw)
			return nil, err
		}
		if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			clear(raw)
			return nil, err
		}
		// Exclusive creation prevents two first-start processes from replacing a key.
		f, e := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			clear(raw)
			return nil, e
		}
		_, e = f.Write(data)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			clear(raw)
			return nil, e
		}
		if ce != nil {
			clear(raw)
			return nil, ce
		}
	} else if err != nil {
		return nil, err
	} else {
		raw, err = Unwrap(passphrase, "akasha/master/v1", data)
		if err != nil {
			return nil, err
		}
	}
	defer clear(raw)
	if len(raw) != 32 {
		return nil, errors.New("invalid master key length")
	}
	v := &Vault{root: root}
	copy(v.master[:], raw)
	return v, nil
}
func (v *Vault) Close()           { v.mu.Lock(); defer v.mu.Unlock(); clear(v.master[:]) }
func keyAD(p core.Project) string { return "akasha/project-key/v1/" + p.SpaceID + "/" + p.ID }
func (v *Vault) keyPath(p core.Project) (string, error) {
	if core.ValidateID(p.SpaceID) != nil || core.ValidateID(p.ID) != nil {
		return "", errors.New("invalid project id")
	}
	return filepath.Join(v.root, "vault", "projects", p.SpaceID, p.ID+".akasha-key"), nil
}
func (v *Vault) ProjectKey(p core.Project) (Key, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var key Key
	path, err := v.keyPath(p)
	if err != nil {
		return key, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		raw, e := random(32)
		if e != nil {
			return key, e
		}
		defer clear(raw)
		copy(key[:], raw)
		sealed, e := Seal(v.master, keyAD(p), raw)
		if e != nil {
			return key, e
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return key, err
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return key, e
		}
		_, e = f.Write(sealed)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return key, e
		}
		return key, ce
	}
	if err != nil {
		return key, err
	}
	raw, err := Open(v.master, keyAD(p), data)
	if err != nil {
		return key, err
	}
	defer clear(raw)
	if len(raw) != 32 {
		return key, errors.New("invalid project key")
	}
	copy(key[:], raw)
	return key, nil
}
func (v *Vault) Export(p core.Project, passphrase []byte) ([]byte, error) {
	key, err := v.ProjectKey(p)
	if err != nil {
		return nil, err
	}
	defer clear(key[:])
	return Wrap(passphrase, keyAD(p), key[:])
}
func (v *Vault) Import(p core.Project, passphrase, data []byte) error {
	raw, err := Unwrap(passphrase, keyAD(p), data)
	if err != nil {
		return err
	}
	defer clear(raw)
	if len(raw) != 32 {
		return errors.New("invalid project key length")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	path, err := v.keyPath(p)
	if err != nil {
		return err
	}
	if prior, e := os.ReadFile(path); e == nil {
		old, e := Open(v.master, keyAD(p), prior)
		if e != nil {
			return e
		}
		defer clear(old)
		if string(old) != string(raw) {
			return errors.New("a different project key already exists; use a fresh local data directory")
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	sealed, err := Seal(v.master, keyAD(p), raw)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// An import racing another process must never replace its newly created key.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(sealed)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil {
		return err
	}
	return closed
}
func recordAD(p core.Project, entity string) string {
	return "akasha/record/v1/" + p.SpaceID + "/" + p.ID + "/" + entity
}
func EncryptBlob(key Key, p core.Project, entity string, plain []byte) ([]byte, error) {
	return Seal(key, recordAD(p, entity), plain)
}
func DecryptBlob(key Key, p core.Project, r core.Record) ([]byte, error) {
	return Open(key, recordAD(p, r.Entity), r.Blob)
}
func (v *Vault) Put(ctx context.Context, s *core.Store, author, name string, secret []byte, expected []string) (core.Record, error) {
	if name == "" || len(name) > 200 || len(secret) == 0 || len(secret) > 65536 {
		return core.Record{}, errors.New("secret name and up to 64 KiB required")
	}
	key, err := v.ProjectKey(s.Project)
	if err != nil {
		return core.Record{}, err
	}
	defer clear(key[:])
	entity := "secrets/" + core.Hash(name)
	blob, err := EncryptBlob(key, s.Project, entity, secret)
	if err != nil {
		return core.Record{}, err
	}
	r, _, err := s.Put(ctx, author, core.WriteInput{Entity: entity, Kind: "secret", Title: name, Blob: blob, Expected: expected})
	return r, err
}
func (v *Vault) Get(ctx context.Context, s *core.Store, id string) ([]byte, error) {
	r, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Kind != "secret" || r.Deleted {
		return nil, core.ErrNotFound
	}
	key, err := v.ProjectKey(s.Project)
	if err != nil {
		return nil, err
	}
	defer clear(key[:])
	return DecryptBlob(key, s.Project, r)
}
