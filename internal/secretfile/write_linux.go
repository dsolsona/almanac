//go:build linux

package secretfile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type tokenFileOps struct {
	write   func(int, []byte) (int, error)
	sync    func(int) error
	syncDir func(int) error
}

func systemTokenFileOps() tokenFileOps {
	return tokenFileOps{write: unix.Write, sync: unix.Fsync, syncDir: unix.Fsync}
}

// TokenFile reserves a destination name, stages complete content privately,
// then links the synced inode into place without replacing an existing file.
type TokenFile struct {
	ctx                            context.Context
	dir, markerFD, tempFD          int
	pathName, markerName, tempName string
	markerStat, tempStat           unix.Stat_t
	ops                            tokenFileOps
	committed, closed              bool
}

func Reserve(ctx context.Context, path string) (*TokenFile, error) {
	if ctx == nil || path == "" || ctx.Err() != nil {
		return nil, ErrInvalid
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrInvalid
	}
	name := filepath.Base(abs)
	dir, err := unix.Open(filepath.Dir(abs), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	var parent unix.Stat_t
	if unix.Fstat(dir, &parent) != nil || parent.Uid != uint32(os.Geteuid()) || parent.Mode&0022 != 0 || parent.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(dir)
		return nil, ErrInvalid
	}
	var target unix.Stat_t
	if err = unix.Fstatat(dir, name, &target, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		_ = unix.Close(dir)
		return nil, ErrInvalid
	} else if !errors.Is(err, unix.ENOENT) {
		_ = unix.Close(dir)
		return nil, ErrInvalid
	}
	hash := sha256.Sum256([]byte(abs))
	markerName := ".almanac-token-reserve-" + hex.EncodeToString(hash[:])
	markerFD, err := unix.Openat(dir, markerName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		_ = unix.Close(dir)
		return nil, ErrInvalid
	}
	t := &TokenFile{ctx: ctx, dir: dir, markerFD: markerFD, tempFD: -1, pathName: name, markerName: markerName, ops: systemTokenFileOps()}
	if unix.Fstat(markerFD, &t.markerStat) != nil || t.markerStat.Uid != uint32(os.Geteuid()) || t.markerStat.Mode&unix.S_IFMT != unix.S_IFREG || unix.Fchmod(markerFD, 0600) != nil || t.ops.sync(markerFD) != nil || t.ops.syncDir(dir) != nil {
		_ = t.Close()
		return nil, ErrInvalid
	}
	return t, nil
}

func (t *TokenFile) Complete(data []byte) error {
	if t == nil || t.closed || t.committed || len(data) == 0 || len(data) > 4096 {
		return ErrInvalid
	}
	if t.ctx.Err() != nil {
		return ErrTokenCancelled
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return ErrInvalid
	}
	t.tempName = ".almanac-token-tmp-" + hex.EncodeToString(random)
	fd, err := unix.Openat(t.dir, t.tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return ErrInvalid
	}
	t.tempFD = fd
	if unix.Fstat(fd, &t.tempStat) != nil || t.tempStat.Uid != uint32(os.Geteuid()) || t.tempStat.Mode&unix.S_IFMT != unix.S_IFREG || unix.Fchmod(fd, 0600) != nil {
		return ErrInvalid
	}
	for len(data) > 0 {
		if t.ctx.Err() != nil {
			return ErrTokenCancelled
		}
		n, e := t.ops.write(fd, data)
		if e != nil || n == 0 {
			return ErrInvalid
		}
		data = data[n:]
	}
	if t.ctx.Err() != nil {
		return ErrTokenCancelled
	}
	if t.ops.sync(fd) != nil {
		return ErrInvalid
	}
	if !t.owns(t.markerName, t.markerStat) || !t.owns(t.tempName, t.tempStat) {
		return ErrInvalid
	}
	if t.ctx.Err() != nil {
		return ErrTokenCancelled
	}
	// linkat is atomic and fails with EEXIST if anyone created the destination
	// after reservation, preserving their file without exposing partial bytes.
	if err = unix.Linkat(t.dir, t.tempName, t.dir, t.pathName, 0); err != nil {
		return ErrInvalid
	}
	var final unix.Stat_t
	if unix.Fstatat(t.dir, t.pathName, &final, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameInode(final, t.tempStat) {
		return ErrTokenCommitUncertain
	}
	t.committed = true
	if t.ops.syncDir(t.dir) != nil {
		return ErrTokenCommitUncertain
	}
	return nil
}

func (t *TokenFile) Close() error {
	if t == nil || t.closed {
		return nil
	}
	t.closed = true
	var closeErr error
	if t.tempFD >= 0 {
		if err := unix.Close(t.tempFD); err != nil {
			closeErr = err
		}
		t.tempFD = -1
	}
	if t.markerFD >= 0 {
		if err := unix.Close(t.markerFD); err != nil {
			closeErr = err
		}
		t.markerFD = -1
	}
	if t.tempName != "" && t.owns(t.tempName, t.tempStat) {
		if err := unix.Unlinkat(t.dir, t.tempName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			closeErr = err
		}
	}
	if t.markerName != "" && t.owns(t.markerName, t.markerStat) {
		if err := unix.Unlinkat(t.dir, t.markerName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			closeErr = err
		}
	}
	if t.dir >= 0 {
		if err := t.ops.syncDir(t.dir); err != nil {
			closeErr = err
		}
		if err := unix.Close(t.dir); err != nil {
			closeErr = err
		}
		t.dir = -1
	}
	if closeErr != nil {
		return ErrInvalid
	}
	return nil
}

func (t *TokenFile) owns(name string, want unix.Stat_t) bool {
	var got unix.Stat_t
	return unix.Fstatat(t.dir, name, &got, unix.AT_SYMLINK_NOFOLLOW) == nil && sameInode(got, want)
}
func sameInode(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode&unix.S_IFMT == b.Mode&unix.S_IFMT
}

// WriteNew is retained for callers that do not need to reserve before dispatch.
func WriteNew(path string, data []byte) error {
	sink, err := Reserve(context.Background(), path)
	if err != nil {
		return err
	}
	defer sink.Close()
	return sink.Complete(data)
}
