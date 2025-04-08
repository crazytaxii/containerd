//go:build linux || darwin

package images

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func relativePath(path, dir1 string, dir2 string) string {
	return filepath.Clean(strings.Replace(path, dir1, dir2, 1))
}

func isSymlink(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}

func isWhiteout(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0 && stat.Rdev == 0
}

func isDev(info os.FileInfo) bool {
	return info.Mode()&os.ModeDevice != 0
}

func isNamedPipe(info os.FileInfo) bool {
	return info.Mode()&os.ModeNamedPipe != 0
}

func newDiffErr(kind, file1, file2 string) error {
	return fmt.Errorf("%s %s is different from %s", kind, file1, file2)
}

func diff(dir1, dir2 string) error {
	return filepath.Walk(dir1, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// ignore the root directory
		if path == dir1 {
			return nil
		}

		_path := relativePath(path, dir1, dir2) // hypothetical file path in dir2
		_info, err := os.Lstat(_path)
		if err != nil {
			return err
		}

		switch {
		case info.Mode().IsRegular():
			if !diffMetadata(info, _info, []diffOpt{withModTime(), withUIDGID()}...) {
				return newDiffErr("file", _path, path)
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			digest1, err := computeDigest(f)
			if err != nil {
				return err
			}

			_f, err := os.Open(_path)
			if err != nil {
				return err
			}
			defer _f.Close()
			digest2, err := computeDigest(_f)
			if err != nil {
				return err
			}
			if digest1 != digest2 {
				return newDiffErr("file", _path, path)
			}
		case isWhiteout(info):
			// the char dev is created by OverlayConvertWhiteout (mknod c 0 0)
			// it was not extracted.
			if !diffMetadata(info, _info, withRdev()) {
				return newDiffErr("whiteout", _path, path)
			}
		case info.IsDir():
			diffOpts := []diffOpt{withUIDGID()}
			if filepath.Base(info.Name()) != "etc" {
				diffOpts = append(diffOpts, withModTime())
			}
			if !diffMetadata(info, _info, diffOpts...) {
				return newDiffErr("directory", _path, path)
			}
		case isNamedPipe(info):
			if !diffMetadata(info, _info, []diffOpt{withModTime(), withUIDGID()}...) {
				return newDiffErr("pipe", _path, path)
			}
		case isDev(info):
			if !diffMetadata(info, _info, []diffOpt{withModTime(), withUIDGID(), withRdev()}...) {
				return newDiffErr("device", _path, path)
			}
		case isSymlink(info):
			if !diffMetadata(info, _info, []diffOpt{withModTime(), withUIDGID()}...) {
				return newDiffErr("symlink", _path, path)
			}
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_target, err := os.Readlink(_path)
			if err != nil {
				return err
			}
			if target != _target {
				return newDiffErr("symlink", _path, path)
			}
		}

		return nil
	})
}

type diffOpt func(meta1, meta2 os.FileInfo) bool

func withModTime() diffOpt {
	return func(meta1, meta2 os.FileInfo) bool {
		return meta1.ModTime().Equal(meta2.ModTime())
	}
}

func withUIDGID() diffOpt {
	return func(meta1, meta2 os.FileInfo) bool {
		stat1, ok := meta1.Sys().(*syscall.Stat_t)
		if !ok {
			return false
		}
		stat2, ok := meta2.Sys().(*syscall.Stat_t)
		if !ok {
			return false
		}
		return stat1.Uid == stat2.Uid && stat1.Gid == stat2.Gid
	}
}

func withRdev() diffOpt {
	return func(meta1, meta2 os.FileInfo) bool {
		stat1, ok := meta1.Sys().(*syscall.Stat_t)
		if !ok {
			return false
		}
		stat2, ok := meta2.Sys().(*syscall.Stat_t)
		if !ok {
			return false
		}
		return stat1.Rdev == stat2.Rdev
	}
}

// diffMetadata returns true if the metadata of two files are the same
func diffMetadata(meta1, meta2 os.FileInfo, opts ...diffOpt) (ok bool) {
	ok = meta1.Mode() == meta2.Mode() &&
		meta1.Size() == meta2.Size() &&
		meta1.IsDir() == meta2.IsDir()

	for _, opt := range opts {
		ok = ok && opt(meta1, meta2)
	}
	return
}
