package doctor

import (
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
)

// SystemUnitDir is the folder of system-wide systemd units on Linux, "" elsewhere.
func SystemUnitDir() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	return "/etc/systemd/system"
}

// LegacySystemUnits lists the systemd units in dir named after the project or the command
// before the rename (switchyard-hub.service, sy-backup.timer, ...). Neither `wy setup` nor
// the post-update step manages system units, so they are moved by hand (rename.md 2.2).
func LegacySystemUnits(dir string) []string {
	if dir == "" {
		return nil
	}
	var prefixes []string
	if brand.Renamed() {
		prefixes = append(prefixes, brand.OldSlug()+"-")
	}
	if brand.CLIRenamed() {
		prefixes = append(prefixes, brand.OldCLI()+"-")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".service") && !strings.HasSuffix(n, ".timer") {
			continue
		}
		for _, p := range prefixes {
			if strings.HasPrefix(n, p) {
				out = append(out, n)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func checkLegacySystemUnits(rep *Report, dir string) {
	units := LegacySystemUnits(dir)
	if len(units) == 0 {
		return
	}
	rep.add(Check{Name: "system units", Level: Warn,
		Detail: "system units from before the rename in " + dir + ": " + strings.Join(units, ", ") +
			" (they still run the old command and are not moved by wy update)",
		Fix: "move them by hand: scripts/rename/RUNBOOK.md, step 6 (system units)"})
}
