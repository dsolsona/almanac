//go:build linux

// Package cliprofile stores CLI sessions in a private, process-locked file.
package cliprofile

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	maxProfileBytes = 96 * 1024
	maxURLBytes     = 2048
	maxCAPEMBytes   = 64 * 1024
	maxSessionBytes = 4096
)

var (
	ErrInvalid         = errors.New("invalid CLI profile")
	ErrUnavailable     = errors.New("CLI profile unavailable")
	ErrCommitUncertain = errors.New("CLI profile commit outcome uncertain")
	ErrUnsupported     = errors.New("CLI profile storage unsupported")
)

type Record struct {
	SchemaVersion int    `json:"schema_version"`
	BackendURL    string `json:"backend_url"`
	CAPEM         string `json:"ca_pem"`
	Session       string `json:"session"`
}

// Locked owns the profile lock and checked parent directory until Close.
type Locked struct {
	ctx    context.Context
	dir    int
	name   string
	lock   int
	closed bool
	ops    fileOps
}

type fileOps struct {
	write   func(int, []byte) (int, error)
	sync    func(int) error
	rename  func(int, string, int, string) error
	syncDir func(int) error
}

func systemFileOps() fileOps {
	return fileOps{
		write:   syscall.Write,
		sync:    syscall.Fsync,
		rename:  syscall.Renameat,
		syncDir: syscall.Fsync,
	}
}

func Open(ctx context.Context, path string, create bool) (*Locked, error) {
	if ctx == nil || path == "" {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrInvalid
	}
	name := filepath.Base(abs)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return nil, ErrInvalid
	}
	if strings.HasSuffix(name, ".lock") || strings.HasPrefix(name, ".almanac-profile-tmp-") {
		return nil, ErrInvalid
	}
	dir, err := syscall.Open(filepath.Dir(abs), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	closeDir := true
	defer func() {
		if closeDir {
			_ = syscall.Close(dir)
		}
	}()
	var dst syscall.Stat_t
	if err = syscall.Fstat(dir, &dst); err != nil || dst.Uid != uint32(os.Geteuid()) || dst.Mode&0022 != 0 || dst.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return nil, ErrInvalid
	}
	fd, e := openChecked(dir, name, false)
	if e == nil {
		_ = syscall.Close(fd)
	} else if !errors.Is(e, syscall.ENOENT) || !create {
		if errors.Is(e, syscall.ENOENT) {
			return nil, ErrUnavailable
		}
		return nil, e
	}
	// Create mode means Open may establish a missing profile; an empty profile is
	// represented as absent until Save, avoiding a partial JSON document.
	lockName := name + ".lock"
	lockFlags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		lockFlags |= syscall.O_CREAT
	}
	lock, err := syscall.Openat(dir, lockName, lockFlags, 0600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
			return nil, ErrInvalid
		}
		return nil, ErrUnavailable
	}
	if err = checkPrivateFile(lock); err != nil {
		_ = syscall.Close(lock)
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			_ = syscall.Close(lock)
			return nil, err
		}
		err = syscall.Flock(lock, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			_ = syscall.Close(lock)
			return nil, ErrUnavailable
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = syscall.Close(lock)
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	// Recheck profile after acquiring the lock to catch replacement or unsafe
	// objects that appeared while another process held it.
	fd, e = openChecked(dir, name, false)
	if e == nil {
		_ = syscall.Close(fd)
	} else if !errors.Is(e, syscall.ENOENT) {
		_ = syscall.Close(lock)
		return nil, e
	} else if !create { /* Load may report absence */
	}
	closeDir = false
	return &Locked{ctx: ctx, dir: dir, name: name, lock: lock, ops: systemFileOps()}, nil
}

func (l *Locked) Load() (Record, bool, error) {
	if err := l.ready(); err != nil {
		return Record{}, false, err
	}
	fd, err := openChecked(l.dir, l.name, false)
	if errors.Is(err, syscall.ENOENT) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	f := os.NewFile(uintptr(fd), "profile")
	if f == nil {
		_ = syscall.Close(fd)
		return Record{}, false, ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > maxProfileBytes {
		return Record{}, false, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, maxProfileBytes+1))
	if ctxErr := l.ctx.Err(); ctxErr != nil {
		return Record{}, false, ctxErr
	}
	if err != nil || len(data) > maxProfileBytes {
		return Record{}, false, ErrInvalid
	}
	r, err := decodeRecord(data)
	if err != nil || validate(r) != nil {
		return Record{}, false, ErrInvalid
	}
	if err = l.ready(); err != nil {
		return Record{}, false, err
	}
	return r, true, nil
}

func (l *Locked) Save(r Record) error {
	if err := l.ready(); err != nil {
		return err
	}
	if err := validate(r); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil || len(data) > maxProfileBytes {
		return ErrInvalid
	}
	tmpName := ".almanac-profile-tmp-" + randomSuffix()
	tmp, err := syscall.Openat(l.dir, tmpName, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrUnavailable
	}
	cleanup := true
	defer func() {
		_ = syscall.Close(tmp)
		if cleanup {
			_ = syscall.Unlinkat(l.dir, tmpName)
		}
	}()
	if err = syscall.Fchmod(tmp, 0600); err != nil {
		return ErrUnavailable
	}
	for len(data) > 0 {
		var n int
		n, err = l.ops.write(tmp, data)
		if err != nil || n == 0 {
			return ErrUnavailable
		}
		data = data[n:]
	}
	if err = l.ops.sync(tmp); err != nil {
		return ErrUnavailable
	}
	if err = l.ready(); err != nil {
		return err
	}
	// Validate destination immediately before atomic replacement.
	dest, e := openChecked(l.dir, l.name, false)
	if e == nil {
		_ = syscall.Close(dest)
	} else if !errors.Is(e, syscall.ENOENT) {
		return e
	}
	if err = l.ops.rename(l.dir, tmpName, l.dir, l.name); err != nil {
		return ErrUnavailable
	}
	cleanup = false
	if err = l.ops.syncDir(l.dir); err != nil {
		return ErrCommitUncertain
	}
	return nil
}

func (l *Locked) Close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	unlockErr := syscall.Flock(l.lock, syscall.LOCK_UN)
	closeLockErr := syscall.Close(l.lock)
	closeDirErr := syscall.Close(l.dir)
	if unlockErr != nil || closeLockErr != nil || closeDirErr != nil {
		return ErrUnavailable
	}
	return nil
}

func (l *Locked) ready() error {
	if l == nil || l.closed {
		return ErrUnavailable
	}
	if err := l.ctx.Err(); err != nil {
		return err
	}
	return nil
}

func openChecked(dir int, name string, create bool) (int, error) {
	flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags |= syscall.O_CREAT | syscall.O_EXCL | syscall.O_WRONLY
	}
	fd, err := syscall.Openat(dir, name, flags, 0600)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return -1, err
		}
		return -1, ErrInvalid
	}
	if err = checkPrivateFile(fd); err != nil {
		_ = syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

func checkPrivateFile(fd int) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return ErrInvalid
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&0777 != 0600 {
		return ErrInvalid
	}
	return nil
}

func validate(r Record) error {
	if r.SchemaVersion != 1 || r.BackendURL == "" || len(r.BackendURL) > maxURLBytes || !utf8.ValidString(r.BackendURL) || len(r.CAPEM) > maxCAPEMBytes || !utf8.ValidString(r.CAPEM) || len(r.Session) > maxSessionBytes {
		return ErrInvalid
	}
	for _, b := range []byte(r.Session) {
		if b < 0x21 || b > 0x7e || strings.ContainsRune(";\\,\"", rune(b)) {
			return ErrInvalid
		}
	}
	return nil
}

func decodeRecord(data []byte) (Record, error) {
	value := jsontext.Value(data)
	if value.Kind() != jsontext.KindBeginObject || !value.IsValid() {
		return Record{}, ErrInvalid
	}
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 4 {
		return Record{}, ErrInvalid
	}
	version, okVersion := fields["schema_version"]
	backend, okBackend := fields["backend_url"]
	ca, okCA := fields["ca_pem"]
	session, okSession := fields["session"]
	if !okVersion || !okBackend || !okCA || !okSession || version.Kind() != jsontext.KindNumber || backend.Kind() != jsontext.KindString || ca.Kind() != jsontext.KindString || session.Kind() != jsontext.KindString {
		return Record{}, ErrInvalid
	}
	var r Record
	if json.Unmarshal(version, &r.SchemaVersion) != nil || json.Unmarshal(backend, &r.BackendURL) != nil || json.Unmarshal(ca, &r.CAPEM) != nil || json.Unmarshal(session, &r.Session) != nil {
		return Record{}, ErrInvalid
	}
	return r, nil
}

func randomSuffix() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b[:])
}
