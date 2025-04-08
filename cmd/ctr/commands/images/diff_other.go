//go:build !linux && !darwin

package images

import (
	"fmt"
	"runtime"
)

func diff(_, _ string) error {
	return fmt.Errorf("unsupported platform %s/%s", runtime.GOOS, runtime.GOARCH)
}
