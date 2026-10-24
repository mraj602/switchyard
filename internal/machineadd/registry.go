package machineadd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/update"
)

// RegistryFile is the operator's list of added machines inside the state folder
// (machines.md 2, 4.2 step 10). It is never sent anywhere.
const RegistryFile = "machines.json"

// Entry is one machine the operator added.
type Entry struct {
	Name    string `json:"name"`
	Machine string `json:"machine"`
	// SSH is the host word as the person typed it ([user@]host or an alias).
	SSH   string `json:"ssh"`
	Reach string `json:"reach"`
	// Port is the forward port on the remote (forward only); Socket the remote socket path
	// with --forward-socket.
	Port   int    `json:"port,omitempty"`
	Socket string `json:"socket,omitempty"`
	// ForwardTarget is the right side of the RemoteForward (the hub as the operator reaches it).
	ForwardTarget string `json:"forward_target,omitempty"`
	// ForwardSet is the backup set that wrote the forward block (wy setup --undo can also
	// take it out).
	ForwardSet string `json:"forward_set,omitempty"`
	// ForwardAdopted says the person's own config already had the forward: nothing was written.
	ForwardAdopted bool `json:"forward_adopted,omitempty"`
	// PullTarget is the hub's pull target id (pull only).
	PullTarget string `json:"pull_target,omitempty"`
	// HubURL is the hub address the remote uses (direct, forward).
	HubURL    string `json:"hub_url,omitempty"`
	Hub       bool   `json:"hub,omitempty"` // this machine is the hub host (updated first)
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	SyVersion string `json:"sy_version"`
	AddedAt   string `json:"added_at"`
	UpdatedAt string `json:"updated_at"`
}

// Registry is <state>/machines.json.
type Registry struct {
	V        int     `json:"v"`
	Machines []Entry `json:"machines"`

	path string
}

// RegistryPath is <state>/machines.json.
func RegistryPath(stateDir string) string { return filepath.Join(stateDir, RegistryFile) }

// LoadRegistry reads the registry; a missing file is an empty registry.
func LoadRegistry(stateDir string) (*Registry, error) {
	r := &Registry{V: 1, path: RegistryPath(stateDir)}
	b, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("%s is not readable: %w (move it away to start a new registry)", r.path, err)
	}
	if r.V != 1 {
		return nil, fmt.Errorf("%s has version %d; this wy reads version 1", r.path, r.V)
	}
	return r, nil
}

// Save writes the registry atomically (0600).
func (r *Registry) Save() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	sort.SliceStable(r.Machines, func(i, j int) bool { return r.Machines[i].Name < r.Machines[j].Name })
	if r.Machines == nil {
		r.Machines = []Entry{}
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(r.path, append(b, '\n'), 0o600)
}

// Find returns the entry with this name, else the one with this machine id, else the one
// added with this SSH host word.
func (r *Registry) Find(key string) (Entry, bool) {
	for _, pass := range []func(Entry) bool{
		func(e Entry) bool { return e.Name == key },
		func(e Entry) bool { return e.Machine == key },
		func(e Entry) bool { return e.SSH == key },
	} {
		for _, e := range r.Machines {
			if pass(e) {
				return e, true
			}
		}
	}
	return Entry{}, false
}

// Upsert stores e, replacing the entry with the same machine id (the name is only a label:
// two machines may be called alike, see UniqueName). An entry without an id is matched by
// name. AddedAt is kept from the old entry.
func (r *Registry) Upsert(e Entry, now time.Time) {
	ts := now.UTC().Format(time.RFC3339)
	e.UpdatedAt = ts
	for i, old := range r.Machines {
		if (e.Machine != "" && old.Machine == e.Machine) || (e.Machine == "" && old.Machine == "" && old.Name == e.Name) {
			e.AddedAt = old.AddedAt
			if e.AddedAt == "" {
				e.AddedAt = ts
			}
			r.Machines[i] = e
			return
		}
	}
	e.AddedAt = ts
	r.Machines = append(r.Machines, e)
}

// FindMachine returns the entry with this machine id.
func (r *Registry) FindMachine(id string) (Entry, bool) {
	for _, e := range r.Machines {
		if id != "" && e.Machine == id {
			return e, true
		}
	}
	return Entry{}, false
}

// NameTaken reports whether another machine (a different id) already has this name.
func (r *Registry) NameTaken(name, machine string) bool {
	for _, e := range r.Machines {
		if e.Name == name && e.Machine != machine {
			return true
		}
	}
	return false
}

// UniqueName returns name when no other machine has it, else name with a short suffix of
// the machine id ("ubuntu-4k2z"), and a number when even that is taken.
func (r *Registry) UniqueName(name, machine string) string {
	if !r.NameTaken(name, machine) {
		return name
	}
	id := strings.TrimPrefix(machine, "m_")
	if len(id) > 4 {
		id = strings.ToLower(id[len(id)-4:])
	}
	cand := name + "-" + id
	for n := 2; r.NameTaken(cand, machine); n++ {
		cand = fmt.Sprintf("%s-%s-%d", name, id, n)
	}
	return cand
}

// Remove drops the entry with this name; false when there was none.
func (r *Registry) Remove(name string) bool {
	for i, e := range r.Machines {
		if e.Name == name {
			r.Machines = append(r.Machines[:i], r.Machines[i+1:]...)
			return true
		}
	}
	return false
}

// UsedPorts are the forward ports of the other registered machines reached with the same
// SSH host word. A forward listens on its own remote, so another machine's port is no
// reason to avoid a port here.
func (r *Registry) UsedPorts(machine, sshHost string) map[int]bool {
	out := map[int]bool{}
	for _, e := range r.Machines {
		if e.Machine != machine && e.SSH == sshHost && e.Port > 0 {
			out[e.Port] = true
		}
	}
	return out
}

// FleetRegistry is the registry as `wy update --machines` reads it (update.Registry).
// HubAdmin is the admin channel prefix: a registry machine whose SSH host is the host of an
// `ssh <host> …` prefix is the hub host, updated first (machines.md 7.4 step 1).
type FleetRegistry struct {
	StateDir string
	HubAdmin string
}

// Machines implements update.Registry.
func (f FleetRegistry) Machines() ([]update.FleetMachine, error) {
	r, err := LoadRegistry(f.StateDir)
	if err != nil {
		return nil, err
	}
	hubHost := AdminSSHHost(f.HubAdmin)
	out := make([]update.FleetMachine, 0, len(r.Machines))
	for _, e := range r.Machines {
		out = append(out, update.FleetMachine{Name: e.Name, Target: e.SSH, Hub: e.Hub || (hubHost != "" && hubHost == e.SSH),
			OS: e.OS, Arch: e.Arch})
	}
	return out, nil
}

var _ update.Registry = FleetRegistry{}
