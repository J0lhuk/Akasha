// Package embedding performs local ONNX inference. Networking is confined to
// the explicit model-install operation; opening and querying never downloads.
package embedding

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const ModelID = "multilingual-e5-small-int8-761b726d-mean-v1"
const Dimension = 384
const SequenceLength = 256

type Installation struct {
	Model            string            `json:"model"`
	Source           string            `json:"source"`
	Revision         string            `json:"revision"`
	Dimension        int               `json:"dimension"`
	Runtime          string            `json:"runtime"`
	TokenizerRuntime string            `json:"tokenizer_runtime"`
	Platform         string            `json:"platform"`
	Files            map[string]string `json:"files"`
	Created          string            `json:"created"`
}

func Directory(data string) string { return filepath.Join(data, "models", ModelID) }
func Info(data string) (Installation, error) {
	var info Installation
	b, err := os.ReadFile(filepath.Join(Directory(data), "installation.json"))
	if err != nil {
		return info, err
	}
	if err = json.Unmarshal(b, &info); err != nil {
		return info, err
	}
	if info.Model != ModelID || info.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		return info, errors.New("embedding installation does not match this model/platform")
	}
	ortName, tokName := "libonnxruntime.so.1.24.1", "libtokenizers.so"
	if runtime.GOOS == "windows" {
		ortName, tokName = "onnxruntime.dll", "tokenizers.dll"
	}
	if info.Dimension != Dimension || info.Runtime != ortName || info.TokenizerRuntime != tokName ||
		len(info.Files[ortName]) != 64 || len(info.Files[tokName]) != 64 {
		return info, errors.New("embedding installation runtime identity is invalid")
	}
	return info, nil
}
func hashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func Verify(data string) (Installation, error) {
	info, err := Info(data)
	if err != nil {
		return info, err
	}
	for name, want := range info.Files {
		if filepath.Base(name) != name {
			return info, errors.New("invalid installed model path")
		}
		actual, e := hashFile(filepath.Join(Directory(data), name))
		if e != nil {
			return info, e
		}
		if actual != want {
			return info, errors.New("embedding file checksum mismatch: " + name)
		}
	}
	if info.Files["model.onnx"] != "f80102d3f2a1229f387d3c81909990d8945513e347b0eab049f7de3c6f98c193" || info.Files["tokenizer.json"] != "0b44a9d7b51c3c62626640cda0e2c2f70fdacdc25bbbd68038369d14ebdf4c39" {
		return info, errors.New("unexpected embedding model identity")
	}
	return info, nil
}

func download(ctx context.Context, url, dest, want string, max int64) error {
	if h, e := hashFile(dest); e == nil && h == want {
		return nil
	}
	client := &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 10 {
			return errors.New("unsafe model download redirect")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Akasha/0.2 local-model-installer")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("model download HTTP %d", resp.StatusCode)
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".download-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(f, h), io.LimitReader(resp.Body, max+1), make([]byte, 128<<10))
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if n > max {
		return errors.New("model download exceeds declared size limit")
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return errors.New("download checksum mismatch")
	}
	return os.Rename(temp, dest)
}

func extractRuntime(archive, dir string, keep func(string) bool) error {
	write := func(name string, reader io.Reader, size int64) error {
		if !keep(name) {
			return nil
		}
		if size < 0 || size > 128<<20 {
			return errors.New("runtime file too large")
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(reader, size+1))
		ce := f.Close()
		if err != nil {
			return err
		}
		if ce != nil {
			return ce
		}
		if n != size {
			return errors.New("runtime entry size mismatch")
		}
		return nil
	}
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, file := range zr.File {
			if file.FileInfo().IsDir() {
				continue
			}
			name := filepath.Base(file.Name)
			if !keep(name) {
				continue
			}
			if !file.Mode().IsRegular() {
				return errors.New("nonregular runtime entry")
			}
			r, e := file.Open()
			if e != nil {
				return e
			}
			e = write(name, r, int64(file.UncompressedSize64))
			r.Close()
			if e != nil {
				return e
			}
		}
	} else {
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			header, e := tr.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			if header.Typeflag != tar.TypeReg {
				continue
			}
			if e = write(filepath.Base(header.Name), tr, header.Size); e != nil {
				return e
			}
		}
	}
	return nil
}

func Install(ctx context.Context, data string, progress func(string)) (Installation, error) {
	if runtime.GOARCH != "amd64" || (runtime.GOOS != "windows" && runtime.GOOS != "linux") {
		return Installation{}, errors.New("bundled embedding setup currently supports Windows/Linux amd64")
	}
	if info, err := Verify(data); err == nil {
		return info, nil
	}
	dir := Directory(data)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Installation{}, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "install.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Installation{}, errors.New("model installation already running or interrupted; inspect install.lock")
	}
	lock.Close()
	defer os.Remove(filepath.Join(dir, "install.lock"))
	if progress == nil {
		progress = func(string) {}
	}
	base := "https://huggingface.co/Xenova/multilingual-e5-small/resolve/761b726dd34fb83930e26aab4e9ac3899aa1fa78/"
	for _, file := range []struct {
		name, url, sha string
		size           int64
	}{{"model.onnx", base + "onnx/model_quantized.onnx", "f80102d3f2a1229f387d3c81909990d8945513e347b0eab049f7de3c6f98c193", 118308185}, {"tokenizer.json", base + "tokenizer.json", "0b44a9d7b51c3c62626640cda0e2c2f70fdacdc25bbbd68038369d14ebdf4c39", 17082730}} {
		progress("Downloading/verifying " + file.name)
		if err = download(ctx, file.url, filepath.Join(dir, file.name), file.sha, file.size); err != nil {
			return Installation{}, err
		}
	}
	ortArchive, ortSHA, ortName := "onnxruntime-linux-x64-1.24.1.tgz", "9142552248b735920f9390027e4512a2cacf8946a1ffcbe9071a5c210531026f", "libonnxruntime.so.1.24.1"
	tokArchive, tokSHA, tokName := "libtokenizers-x86_64-unknown-linux-gnu.tar.gz", "8c154b6411f96dc299fd6a18520c074ac1469940f90ded0009ffba28066eeb62", "libtokenizers.so"
	if runtime.GOOS == "windows" {
		ortArchive, ortSHA, ortName = "onnxruntime-win-x64-1.24.1.zip", "c72736c16442cad417ee4f45e3e2fbd10778e70a0fc1cb0e86f028d766e525af", "onnxruntime.dll"
		tokArchive, tokSHA, tokName = "libtokenizers-x86_64-pc-windows-msvc.tar.gz", "4df138fec675462045b724e4406a4a35c265add8025ab0f1f0af3575c66e1c09", "tokenizers.dll"
	}
	for _, file := range []struct{ archive, url, sha, name string }{{ortArchive, "https://github.com/microsoft/onnxruntime/releases/download/v1.24.1/" + ortArchive, ortSHA, ortName}, {tokArchive, "https://releases.amikos.tech/pure-tokenizers/rust-v0.1.5/" + tokArchive, tokSHA, tokName}} {
		progress("Downloading/verifying " + file.archive)
		archive := filepath.Join(dir, file.archive)
		if err = download(ctx, file.url, archive, file.sha, 100<<20); err != nil {
			return Installation{}, err
		}
		if err = extractRuntime(archive, dir, func(name string) bool {
			return name == file.name || name == "onnxruntime_providers_shared.dll" || strings.HasPrefix(strings.ToUpper(name), "LICENSE")
		}); err != nil {
			return Installation{}, err
		}
		if err = os.Remove(archive); err != nil {
			return Installation{}, err
		}
	}
	info := Installation{Model: ModelID, Source: "Xenova/multilingual-e5-small (ONNX export of intfloat/multilingual-e5-small)", Revision: "761b726dd34fb83930e26aab4e9ac3899aa1fa78", Dimension: Dimension, Runtime: ortName, TokenizerRuntime: tokName, Platform: runtime.GOOS + "/" + runtime.GOARCH, Files: map[string]string{}, Created: time.Now().UTC().Format(time.RFC3339)}
	for _, name := range []string{"model.onnx", "tokenizer.json", ortName, tokName} {
		h, e := hashFile(filepath.Join(dir, name))
		if e != nil {
			return info, e
		}
		info.Files[name] = h
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return info, err
	}
	if err = os.WriteFile(filepath.Join(dir, "installation.json"), b, 0600); err != nil {
		return info, err
	}
	return info, nil
}
