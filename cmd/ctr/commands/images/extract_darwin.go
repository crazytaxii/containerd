package images

import (
	"context"
	"io"
	"os"

	"github.com/containerd/containerd/archive"
	"github.com/containerd/containerd/archive/compression"
)

func extract(ctx context.Context, r io.Reader, dst string) error {
	ur, err := compression.DecompressStream(r)
	if err != nil {
		return err
	}
	defer ur.Close()

	opts := []archive.ApplyOpt{}
	if os.Getuid() != 0 {
		opts = append(opts, archive.WithNoSameOwner())
	}
	_, err = archive.Apply(ctx, dst, ur, opts...)
	return err
}
