//go:build !linux && !darwin

package images

import (
	"context"
	"fmt"
	"io"
	"runtime"
)

func extract(_ context.Context, _ io.Reader, _ string) error {
	return fmt.Errorf("unsupported platform %s/%s", runtime.GOOS, runtime.GOARCH)
}
