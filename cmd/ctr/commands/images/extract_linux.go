package images

import (
	"context"
	"io"

	"github.com/containerd/containerd/archive"
	"github.com/containerd/containerd/archive/compression"
	"github.com/moby/sys/userns"
)

func extract(ctx context.Context, r io.Reader, dst string) error {
	ur, err := compression.DecompressStream(r)
	if err != nil {
		return err
	}
	defer ur.Close()

	opts := []archive.ApplyOpt{}
	// OverlayConvertWhiteout (mknod c 0 0) doesn't work in userns.
	// https://github.com/containerd/containerd/issues/3762
	if !userns.RunningInUserNS() {
		opts = append(opts, archive.WithConvertWhiteout(archive.OverlayConvertWhiteout))
	}
	_, err = archive.Apply(ctx, dst, ur, opts...)
	return err
}
