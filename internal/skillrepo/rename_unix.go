//go:build !windows

package skillrepo

import (
	"context"
	"os"
)

func renameDirectory(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(from, to)
}
