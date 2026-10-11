//go:build windows

package skillrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestInstallRenameWaitsForWindowsFileHandle(t *testing.T) {
	root := t.TempDir()
	stage, destination := filepath.Join(root, "stage"), filepath.Join(root, "installed")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(stage, "SKILL.md")
	if err := os.WriteFile(file, []byte("verified procedure"), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(file)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, destination); err == nil {
		windows.CloseHandle(handle)
		t.Fatal("fixture did not lock the directory rename")
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(80 * time.Millisecond)
		windows.CloseHandle(handle)
		close(released)
	}()
	defer func() { <-released }()
	if err := renameDirectory(context.Background(), stage, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "SKILL.md"))
	if err != nil || string(data) != "verified procedure" {
		t.Fatal("installation lost bytes", err)
	}
}
