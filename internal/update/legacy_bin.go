package update

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mraj602/watchyard/internal/brand"
)

// LegacyBinaryName is the executable's file name on goos under the command's name before a
// rename (docs/spec/rename.md 2.13).
func LegacyBinaryName(goos string) string {
	if goos == "windows" {
		return brand.OldCLI() + ".exe"
	}
	return brand.OldCLI()
}

// MoveLegacyBinary runs after an update when the command was renamed: the binary in exe's
// folder gets the current name, and the old name stays as a link to it (a copy on Windows),
// so hooks, units, launchers and authorized_keys lines written for the old name keep working
// until `wy setup` rewrites them (docs/spec/rename.md 2.13). exe is the running binary. It
// returns what it did ("" when there was nothing to do).
func MoveLegacyBinary(exe, goos string) (string, error) {
	if !brand.CLIRenamed() || exe == "" {
		return "", nil
	}
	dir := filepath.Dir(exe)
	cur := filepath.Join(dir, BinaryName(goos))
	old := filepath.Join(dir, LegacyBinaryName(goos))
	did := ""
	if filepath.Base(exe) == filepath.Base(old) {
		// Running under the old name (an older wy updated itself): take the current name.
		if err := os.Rename(exe, cur); err != nil {
			return "", fmt.Errorf("rename %s to %s: %w", exe, cur, err)
		}
		did = "renamed " + filepath.Base(old) + " to " + filepath.Base(cur)
	} else {
		fi, err := os.Lstat(old)
		if err != nil || !fi.Mode().IsRegular() {
			return "", nil // no old binary, or already a link
		}
	}
	if _, err := os.Stat(cur); err != nil {
		return did, err
	}
	if err := linkOrCopy(cur, old, goos); err != nil {
		return did, err
	}
	if did != "" {
		did += ", "
	}
	if goos == "windows" {
		return did + filepath.Base(old) + " is a copy of " + filepath.Base(cur), nil
	}
	return did + filepath.Base(old) + " now links to " + filepath.Base(cur), nil
}

// linkOrCopy replaces dst with a relative symlink to src (a copy on Windows), atomically.
func linkOrCopy(src, dst, goos string) error {
	tmp := dst + ".new"
	_ = os.Remove(tmp)
	if goos != "windows" {
		if err := os.Symlink(filepath.Base(src), tmp); err != nil {
			return err
		}
		return os.Rename(tmp, dst)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
