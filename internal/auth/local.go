package auth

import (
	"errors"
	"os"
	"path/filepath"

	"akasha/internal/core"
)

func SaveCredential(file string, plain []byte) error {
	sealed, err := protect(plain)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(sealed)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
func ReadCredential(file string) (string, error) {
	b, e := os.ReadFile(file)
	if e != nil {
		return "", e
	}
	if len(b) > 65536 {
		return "", errors.New("credential file too large")
	}
	plain, e := unprotect(b)
	if e != nil {
		return "", e
	}
	defer clear(plain)
	if len(plain) != 64 {
		return "", errors.New("invalid session credential")
	}
	return string(plain), nil
}

func LocalToken(root string) (string, error) {
	file := filepath.Join(root, "auth", "owner.token")
	data, err := os.ReadFile(file)
	if err == nil {
		plain, e := unprotect(data)
		if e != nil {
			return "", e
		}
		defer clear(plain)
		if len(plain) != 64 {
			return "", errors.New("invalid local token file")
		}
		return string(plain), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	token := core.NewID() + core.NewID()
	sealed, err := protect([]byte(token))
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return LocalToken(root)
	}
	if err != nil {
		return "", err
	}
	_, err = f.Write(sealed)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return "", err
	}
	return token, ce
}
