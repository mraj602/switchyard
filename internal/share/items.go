package share

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mraj602/watchyard/internal/setup/model"
)

// Item is one row of the setup.md 5.1 table that is shared by linking.
type Item = model.ItemSpec

// Items is the item table (5.1) as data. Adding a row is a spec change; it starts from the
// contract table in model so the two cannot drift.
var Items = append([]Item(nil), model.ShareItems...)

// Lookup returns the row for a name relative to the config folder.
func Lookup(name string) (Item, bool) {
	for _, it := range Items {
		if it.Name == name {
			return it, true
		}
	}
	return Item{}, false
}

// ItemsFor returns the rows controlled by a switch, in table order.
func ItemsFor(s model.Switch) []Item {
	var out []Item
	for _, it := range Items {
		if it.Switch == s {
			out = append(out, it)
		}
	}
	return out
}

// NeverShared reports whether name (relative to a config folder, or a base name) is on the
// never-shared list of 5.1, including `*.lock` and anything not in the item table.
func NeverShared(name string) bool {
	base := filepath.Base(name)
	if strings.HasSuffix(base, ".lock") {
		return true
	}
	for _, n := range model.NeverShared {
		if base == n {
			return true
		}
	}
	_, ok := Lookup(name)
	return !ok
}

// LinkKind is the mechanism for an item on an OS (5.1 last column): a symlink on Unix; on
// Windows a junction for directories and a symlink for files if symlinks are allowed, else a
// synced copy.
func LinkKind(it Item, goos string, symlinksAllowed bool) string {
	if goos != "windows" {
		return model.LinkSymlink
	}
	if it.Kind == model.KindDir {
		return model.LinkJunction
	}
	if symlinksAllowed {
		return model.LinkSymlink
	}
	return model.LinkCopy
}

// HostLinkKind is LinkKind for the running OS.
func HostLinkKind(it Item, symlinksAllowed bool) string {
	return LinkKind(it, runtime.GOOS, symlinksAllowed)
}
