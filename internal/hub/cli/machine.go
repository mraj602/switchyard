package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
)

// Machine is `wy hub machine show <id> [--json] [--db F]` (machines.md 4.5): one machine's read
// model (api.yaml Machine, removed machines included) for the admin channel, which is how
// `wy machine add` checks a machine reached the hub.
func Machine(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "show" {
		fmt.Fprintln(stderr, "usage: wy hub machine show <id> [--json] [--db hub.db]")
		return ErrUsage
	}
	fs, db := newFlags("machine show", stderr)
	asJSON := fs.Bool("json", false, "print JSON (api.yaml Machine)")
	pos, err := parse(fs, args[1:])
	if err != nil || len(pos) != 1 {
		fmt.Fprintln(stderr, "usage: wy hub machine show <id> [--json] [--db hub.db]")
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openStore(ctx, *db, nil)
	if err != nil {
		return err
	}
	defer st.Close()
	m, err := st.MachineByID(ctx, pos[0], time.Now())
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("wy hub machine: no machine %s", pos[0])
	} else if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(m)
	}
	str := func(p *string) string {
		if p == nil || *p == "" {
			return "-"
		}
		return *p
	}
	fmt.Fprintf(stdout, "%s  %s\n", m.ID, m.Name)
	fmt.Fprintf(stdout, "  os/arch     %s/%s\n", str(m.OS), str(m.Arch))
	fmt.Fprintf(stdout, "  reach       %s\n", str(m.Reach))
	fmt.Fprintf(stdout, "  wy          %s (%s)\n", str(m.WyVersion), m.VersionStatus)
	fmt.Fprintf(stdout, "  last seen   %s\n", ms(int64(m.LastSeen)))
	fmt.Fprintf(stdout, "  last record %s\n", ms(int64(m.LastRecord)))
	backlog := "-"
	if m.Backlog != nil {
		backlog = fmt.Sprintf("%d (reported %s, oldest %s)", *m.Backlog, ms(int64(m.BacklogAt)), ms(int64(m.BacklogOldest)))
	}
	fmt.Fprintf(stdout, "  backlog     %s\n", backlog)
	if m.Held > 0 {
		fmt.Fprintf(stdout, "  held        %d records (hub too old)\n", m.Held)
	}
	fmt.Fprintf(stdout, "  direct push %v\n", m.DirectPush)
	if m.Pull != nil {
		fmt.Fprintf(stdout, "  pull        %s %s every %ds, %s\n", m.Pull.Name, m.Pull.Target, m.Pull.IntervalS, m.Pull.State)
	}
	if m.Silent {
		fmt.Fprintln(stdout, "  silent")
	}
	if m.RemovedAt != 0 {
		fmt.Fprintf(stdout, "  removed     %s\n", ms(int64(m.RemovedAt)))
	}
	var slots []string
	for _, s := range m.Slots {
		slots = append(slots, s.Folder)
	}
	if len(slots) > 0 {
		fmt.Fprintf(stdout, "  slots       %s\n", strings.Join(slots, ", "))
	}
	return nil
}
