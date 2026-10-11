//go:build !windows

package auth

func protect(data []byte) ([]byte, error)   { return append([]byte(nil), data...), nil }
func unprotect(data []byte) ([]byte, error) { return append([]byte(nil), data...), nil }
