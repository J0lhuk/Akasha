//go:build windows

package skillrepo

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Windows scanners/editors can briefly hold a child without FILE_SHARE_DELETE.
// Retry only the specific sharing/access errors, with a bounded delay and no
// permission changes. Other errors (including a missing path) return at once.
func renameDirectory(ctx context.Context, from, to string) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := os.Rename(from, to)
		if err == nil || attempt == 7 || !(errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)) {
			return err
		}
		timer := time.NewTimer((10 * time.Millisecond) << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
