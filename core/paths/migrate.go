package paths

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// legacyDirName is the historic single-tree gil base under HOME. New
// installs live under XDG; old ones get migrated lazily on first daemon
// start.
const legacyDirName = ".gil"

// migrationStamp is written under the legacy ~/.gil tree once
// MigrateLegacyTilde has run, so subsequent calls become a cheap no-op.
const migrationStamp = "MIGRATED"

// renameFunc is the package-level indirection used by MigrateLegacyTilde
// so tests can inject a rename that returns syscall.EXDEV and exercise
// the cross-device fallback path. Production code always uses
// os.Rename.
var renameFunc = os.Rename

// MigrateLegacyTilde performs a one-shot, idempotent migration from the
// pre-XDG single-tree layout (~/.gil/...) into the XDG-derived Layout l.
//
// It is safe to call on every daemon start: if ~/.gil does not exist,
// the migration stamp is already present, or the destination
// directories contain user data, the function returns (false, nil)
// without touching anything.
//
// Mapping (only files/dirs known to the legacy layout are moved):
//
//	~/.gil/sessions/      → l.SessionsDir()       (Data/sessions)
//	~/.gil/sessions.db    → l.SessionsDB()        (Data/sessions.db)
//	~/.gil/gild.sock      → l.Sock()              (State/gild.sock)
//	~/.gil/gild.pid       → l.Pid()               (State/gild.pid)
//	~/.gil/shadow/        → l.ShadowGitBase()     (Data/shadow)
//	~/.gil/users/         → split per user        (each gets its own
//	                                               WithUser sub-layout)
//
// Anything else under ~/.gil is left in place — we do not want to
// silently delete files we don't understand. After a successful run,
// a marker file <legacy>/MIGRATED is written so the next call short-
// circuits.
//
// Migration uses os.Rename first; if that returns EXDEV (cross-device,
// e.g. /home and /var/lib on different mounts) we fall back to a
// recursive copy + delete. This matters in containerised setups where
// HOME and the XDG dirs may live on different volumes.
//
// Returns (migrated, err) where migrated is true iff at least one
// rename actually happened in this call.
func MigrateLegacyTilde(l Layout) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("paths/migrate: resolve home: %w", err)
	}
	legacy := filepath.Join(home, legacyDirName)

	// Fast path: nothing to do.
	if _, err := os.Stat(legacy); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("paths/migrate: stat legacy: %w", err)
	}

	// Already migrated?
	if _, err := os.Stat(filepath.Join(legacy, migrationStamp)); err == nil {
		return false, nil
	}

	// Make sure the XDG tree exists before we move anything in.
	if err := l.EnsureDirs(); err != nil {
		return false, err
	}

	moved := false

	// Per-user subtrees come first so that a stray top-level sessions/
	// dir from a single-user install is still picked up by the generic
	// branch below.
	usersDir := filepath.Join(legacy, "users")
	if entries, err := os.ReadDir(usersDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			userLayout := l.WithUser(e.Name())
			if err := userLayout.EnsureDirs(); err != nil {
				return moved, err
			}
			ok, err := migrateOneTree(filepath.Join(usersDir, e.Name()), userLayout)
			if err != nil {
				return moved, err
			}
			moved = moved || ok
		}
		// Try to remove the now-empty users dir; ignore failure.
		_ = os.Remove(usersDir)
	}

	// Top-level (default user) tree.
	ok, err := migrateOneTree(legacy, l)
	if err != nil {
		return moved, err
	}
	moved = moved || ok

	// Drop the stamp regardless of whether anything was moved this
	// call — the legacy tree existed but had nothing recognisable, and
	// future runs should keep skipping it.
	if err := writeStamp(legacy); err != nil {
		return moved, err
	}
	return moved, nil
}

// migrateOneTree moves the well-known set of files/dirs from src into
// the slots of dst. Unrecognised paths under src are left untouched.
func migrateOneTree(src string, dst Layout) (bool, error) {
	type pair struct {
		from, to string
	}
	plan := []pair{
		{filepath.Join(src, "sessions"), dst.SessionsDir()},
		{filepath.Join(src, "sessions.db"), dst.SessionsDB()},
		{filepath.Join(src, "gild.sock"), dst.Sock()},
		{filepath.Join(src, "gild.pid"), dst.Pid()},
		{filepath.Join(src, "shadow"), dst.ShadowGitBase()},
	}

	moved := false
	for _, p := range plan {
		ok, err := moveIfPresentAndDestEmpty(p.from, p.to)
		if err != nil {
			return moved, err
		}
		moved = moved || ok
	}
	return moved, nil
}

// moveIfPresentAndDestEmpty renames from→to when from exists and to is
// either absent or empty. If renameFunc returns EXDEV, falls back to a
// recursive copy + delete. Returns (true, nil) iff the move happened.
func moveIfPresentAndDestEmpty(from, to string) (bool, error) {
	srcInfo, err := os.Stat(from)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("paths/migrate: stat %s: %w", from, err)
	}

	if !destIsEmpty(to) {
		// Destination already has user data — skip silently. We log
		// nothing here because the caller (gild) will surface this
		// once via slog after the function returns.
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return false, fmt.Errorf("paths/migrate: mkdir parent %s: %w", filepath.Dir(to), err)
	}

	// Best case: same filesystem, just rename.
	if err := renameFunc(from, to); err == nil {
		return true, nil
	} else if !isCrossDevice(err) {
		return false, fmt.Errorf("paths/migrate: rename %s → %s: %w", from, to, err)
	}

	// Cross-device fallback: copy then delete.
	if srcInfo.IsDir() {
		if err := copyDir(from, to); err != nil {
			return false, fmt.Errorf("paths/migrate: copy dir %s → %s: %w", from, to, err)
		}
		if err := os.RemoveAll(from); err != nil {
			return false, fmt.Errorf("paths/migrate: remove src %s after copy: %w", from, err)
		}
		return true, nil
	}
	if err := copyFile(from, to); err != nil {
		return false, fmt.Errorf("paths/migrate: copy file %s → %s: %w", from, to, err)
	}
	if err := os.Remove(from); err != nil {
		return false, fmt.Errorf("paths/migrate: remove src %s after copy: %w", from, err)
	}
	return true, nil
}

// destIsEmpty returns true when path either does not exist or is an
// empty directory. A non-empty file or non-empty dir at path means the
// user has existing XDG data we must not clobber.
func destIsEmpty(path string) bool {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) == 0
}

// isCrossDevice reports whether err indicates a cross-filesystem
// rename (EXDEV). We unwrap LinkError because os.Rename returns one.
func isCrossDevice(err error) bool {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return errors.Is(linkErr.Err, syscall.EXDEV)
	}
	return errors.Is(err, syscall.EXDEV)
}

// copyDir copies src tree onto dst tree, preserving file modes.
// Symlinks are copied as symlinks (not followed).
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm())
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return copyFile(path, target)
		}
	})
}

// copyFile copies a single regular file, preserving permission bits.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// writeStamp drops the MIGRATED marker so subsequent calls become a
// no-op. The body records the wall-clock for forensic value; nothing
// in gil parses it.
func writeStamp(legacy string) error {
	stamp := filepath.Join(legacy, migrationStamp)
	body := fmt.Appendf(nil, "migrated %s\n", time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(stamp, body, 0o600)
}
