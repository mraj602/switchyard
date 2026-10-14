//go:build windows

package share

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const errNotSameDevice syscall.Errno = 17 // ERROR_NOT_SAME_DEVICE

func crossDevice(err error) bool {
	return errors.Is(err, errNotSameDevice) || errors.Is(err, syscall.EXDEV)
}

func openReparse(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

// readJunction returns the print name of a mount-point reparse point.
func readJunction(path string) (string, bool) {
	h, err := openReparse(path, 0)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	buf := make([]byte, windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE)
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_GET_REPARSE_POINT, nil, 0, &buf[0], uint32(len(buf)), &n, nil); err != nil {
		return "", false
	}
	if n < 16 || binary.LittleEndian.Uint32(buf[0:4]) != windows.IO_REPARSE_TAG_MOUNT_POINT {
		return "", false
	}
	subOff := int(binary.LittleEndian.Uint16(buf[8:10]))
	subLen := int(binary.LittleEndian.Uint16(buf[10:12]))
	prOff := int(binary.LittleEndian.Uint16(buf[12:14]))
	prLen := int(binary.LittleEndian.Uint16(buf[14:16]))
	path16 := func(off, l int) string {
		start := 16 + off
		if l == 0 || start+l > len(buf) {
			return ""
		}
		u := make([]uint16, l/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(buf[start+2*i:])
		}
		return windows.UTF16ToString(u)
	}
	if s := path16(prOff, prLen); s != "" {
		return s, true
	}
	return strings.TrimPrefix(path16(subOff, subLen), `\??\`), true
}

// linkInfo detects symlinks and junctions by reparse tag, not by os.ModeSymlink (Go's
// reporting of junctions changed between versions; setup.md 5.7).
func linkInfo(path string) (raw string, junction, isLink bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", false, false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		if raw, err = os.Readlink(path); err == nil {
			return raw, false, true
		}
	}
	if fi.IsDir() || fi.Mode()&os.ModeIrregular != 0 {
		if t, ok := readJunction(path); ok {
			return t, true, true
		}
	}
	return "", false, false
}

func utf16Bytes(s string) []byte {
	u := windows.StringToUTF16(s) // includes the terminating NUL
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

// makeJunction creates the directory path as a junction to target (mklink /J semantics).
func makeJunction(path, target string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return err
	}
	sub := utf16Bytes(`\??\` + abs) // NUL included
	print := utf16Bytes(abs)
	data := make([]byte, 0, 16+len(sub)+len(print))
	hdr := make([]byte, 16)
	binary.LittleEndian.PutUint32(hdr[0:], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(hdr[4:], uint16(8+len(sub)+len(print))) // data length
	binary.LittleEndian.PutUint16(hdr[8:], 0)                             // substitute offset
	binary.LittleEndian.PutUint16(hdr[10:], uint16(len(sub)-2))
	binary.LittleEndian.PutUint16(hdr[12:], uint16(len(sub)))
	binary.LittleEndian.PutUint16(hdr[14:], uint16(len(print)-2))
	data = append(append(append(data, hdr...), sub...), print...)
	h, err := openReparse(path, windows.GENERIC_WRITE)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	defer windows.CloseHandle(h)
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, (*byte)(unsafe.Pointer(&data[0])), uint32(len(data)), nil, 0, &n, nil); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}
