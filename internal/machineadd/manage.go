package machineadd

import (
	"context"
	"errors"
	"fmt"
	"net"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/sshx"
)

// ManageEnv is what remove, repoint and list need.
type ManageEnv struct {
	SSH      *sshx.Runner
	Admin    Admin // nil: no hub view, no hub-side cleanup
	StateDir string
	Home     string
	Command  string
	Now      func() time.Time
	Events   func(Event)
}

func (e *ManageEnv) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *ManageEnv) emit(step, status, text string) {
	if e.Events != nil {
		e.Events(Event{Step: step, Status: status, Text: text})
	}
}

// RemoveOptions are the flags of `wy machine remove`.
type RemoveOptions struct {
	Revoke     bool
	KeepRemote bool
}

// RemoveResult says what remove did.
type RemoveResult struct {
	Name       string   `json:"name"`
	Forward    bool     `json:"forward_removed"`
	PullLine   bool     `json:"authorized_key_removed"`
	PullTarget bool     `json:"pull_target_removed"`
	Revoked    bool     `json:"revoked"`
	Warnings   []string `json:"warnings,omitempty"`
	Next       string   `json:"next"`
}

// Remove is `wy machine remove <name>` (4.5): the forward block, the remote's
// authorized_keys line and the pull target, the registry entry; with Revoke the machine's
// devices on the hub. The remote's wy, outbox and Claude folders stay.
func Remove(ctx context.Context, name string, o RemoveOptions, e *ManageEnv) (RemoveResult, error) {
	reg, err := LoadRegistry(e.StateDir)
	if err != nil {
		return RemoveResult{}, err
	}
	ent, ok := reg.Find(name)
	if !ok {
		return RemoveResult{}, exitf(ExitUsage, "%s is not in %s (wy machine list)", name, RegistryPath(e.StateDir))
	}
	res := RemoveResult{Name: ent.Name, Next: fmt.Sprintf("ssh %s %s uninstall", ent.SSH, RemoteWy)}
	warn := func(s string) {
		res.Warnings = append(res.Warnings, s)
		e.emit("remove", "warn", s)
	}
	// A block for this machine id in watchyard.conf is wy's own (a forward the person's own
	// config had is never written there), so it goes whatever the entry's adopted flag says.
	if ent.ForwardSet != "" || ent.Reach == ReachForward {
		changed, err := RemoveForward(e.Home, e.StateDir, ent.Machine, ent.ForwardSet, e.Command, e.now())
		if err != nil {
			return res, fmt.Errorf("removing the forward block: %w", err)
		}
		res.Forward = changed
		if changed {
			e.emit("forward", "ok", "removed the forward block for "+ent.Name+" from ~/.ssh/watchyard.conf")
		}
	}
	if ent.PullTarget != "" && !o.KeepRemote && e.SSH != nil {
		r := remote{ssh: e.SSH, host: ent.SSH}
		out, err := r.run(ctx, []string{RemoteWy, "machine", "authorize-pull", "--remove", "--target", ent.PullTarget}, nil)
		switch {
		case err != nil:
			warn(fmt.Sprintf("could not remove the hub's key from %s: %v", ent.SSH, err))
		case out.Exit != 0:
			warn(fmt.Sprintf("could not remove the hub's key from %s: %s", ent.SSH, firstLine(out.Stderr)))
		default:
			res.PullLine = true
			e.emit("remote", "ok", "removed the hub's key from ~/.ssh/authorized_keys on "+ent.SSH)
		}
	}
	if e.Admin != nil {
		if ent.PullTarget != "" {
			args := []string{"pull", "remove", pullNameFor(ctx, e.Admin, ent.PullTarget, ent.Name)}
			if o.Revoke {
				args = append(args, "--revoke")
			}
			if _, err := e.Admin.Hub(ctx, args, nil); err != nil {
				warn(err.Error())
			} else {
				res.PullTarget = true
				res.Revoked = o.Revoke
				e.emit("hub", "ok", "removed pull target "+ent.Name+" on the hub")
			}
		} else if o.Revoke {
			if _, err := e.Admin.Hub(ctx, []string{"token", "revoke", ent.Name, "--yes"}, nil); err != nil {
				warn(err.Error())
			} else {
				res.Revoked = true
				e.emit("hub", "ok", "revoked "+ent.Name+"'s token on the hub (its history stays)")
			}
		}
	} else if ent.PullTarget != "" || o.Revoke {
		warn("no admin channel: remove it on the hub with wy hub pull remove / wy hub token revoke")
	}
	reg.Remove(ent.Name)
	if err := reg.Save(); err != nil {
		return res, err
	}
	e.emit("registry", "ok", "removed "+ent.Name+" from the registry. Next: "+res.Next)
	return res, nil
}

// RepointRow is one machine's answer to `wy machine repoint`.
type RepointRow struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, skip, fail
	Detail string `json:"detail,omitempty"`
}

// Repoint is `wy machine repoint <url>` (4.5, 8.3): `wy connect <url> --keep-token` on each
// registered direct and forward machine, 4 at a time. which is nil for all.
func Repoint(ctx context.Context, url string, which []string, e *ManageEnv) ([]RepointRow, error) {
	if err := sshx.CheckWord(url); err != nil {
		return nil, exitf(ExitUsage, "%v", err)
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return nil, exitf(ExitUsage, "repoint wants the new hub address (https://...)")
	}
	reg, err := LoadRegistry(e.StateDir)
	if err != nil {
		return nil, err
	}
	var todo []Entry
	for _, n := range which {
		if _, ok := reg.Find(n); !ok {
			return nil, exitf(ExitUsage, "%s is not in the registry", n)
		}
	}
	for _, ent := range reg.Machines {
		if len(which) > 0 && !contains(which, ent.Name) {
			continue
		}
		todo = append(todo, ent)
	}
	rows := make([]RepointRow, len(todo))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, ent := range todo {
		if ent.Reach == ReachPull {
			rows[i] = RepointRow{Name: ent.Name, Status: "skip", Detail: "pulled by the hub: nothing to change"}
			continue
		}
		wg.Add(1)
		go func(i int, ent Entry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			row := RepointRow{Name: ent.Name, Status: "ok"}
			upd, err := repointOne(ctx, url, ent, e)
			if err != nil {
				row.Status, row.Detail = "fail", err.Error()
			}
			mu.Lock()
			rows[i] = row
			todo[i] = upd
			mu.Unlock()
		}(i, ent)
	}
	wg.Wait()
	failed := 0
	for i, row := range rows {
		e.emit("repoint", row.Status, fmt.Sprintf("%s: %s %s", row.Name, row.Status, row.Detail))
		if row.Status == "ok" && todo[i].Reach != ReachPull {
			reg.Upsert(todo[i], e.now())
		}
		if row.Status == "fail" {
			failed++
		}
	}
	if err := reg.Save(); err != nil {
		return rows, err
	}
	if failed > 0 {
		return rows, exitf(ExitFailed, "%d machine(s) could not be repointed", failed)
	}
	return rows, nil
}

// repointOne repoints one direct or forward machine and returns its updated entry. A
// forward machine needs three things to agree: the target of the forward block (so the new
// hub is what the tunnel reaches), the remote's hub.url and its hub.dial/reach, which are
// passed again and not left to the remote's own config.
func repointOne(ctx context.Context, url string, ent Entry, e *ManageEnv) (Entry, error) {
	r := remote{ssh: e.SSH, host: ent.SSH}
	words := []string{RemoteWy, "connect", url, "--keep-token"}
	upd := ent
	var oldBlock *Forward
	if ent.Reach == ReachForward {
		if ent.Port == 0 && ent.Socket == "" {
			return ent, errors.New("the registry has no forward port for this machine: run wy machine add again")
		}
		target, err := hostPortOf(url)
		if err != nil {
			return ent, err
		}
		f := Forward{Host: HostPattern(ent.SSH), Machine: ent.Machine, Name: ent.Name, Port: ent.Port, Socket: ent.Socket, Target: target}
		remoteURL := url
		if isLoopbackURL(url) && ent.Socket == "" {
			remoteURL = "http://127.0.0.1:" + strconv.Itoa(ent.Port)
		}
		words[2] = remoteURL
		if !(isLoopbackURL(url) && ent.Socket == "") {
			words = append(words, "--dial", f.Dial())
		}
		words = append(words, "--reach", ReachForward)
		upd.HubURL, upd.ForwardTarget = remoteURL, target
		if _, _, owned := OwnedForward(e.Home, ent.Machine); owned && ent.ForwardTarget != target {
			// The block is wy's: point it at the new hub before the remote connects through it.
			old := f
			old.Target = ent.ForwardTarget
			oldBlock = &old
			res, err := WriteForward(e.Home, e.StateDir, f, e.Command, e.now())
			if err != nil {
				return ent, fmt.Errorf("rewriting the forward block: %w", err)
			}
			if res.Set != "" {
				upd.ForwardSet = res.Set
			}
		}
	} else {
		upd.HubURL = url
	}
	for _, w := range words {
		if err := sshx.CheckWord(w); err != nil {
			return ent, err
		}
	}
	out, err := r.run(ctx, words, nil)
	switch {
	case err != nil:
	case out.Exit != 0:
		err = errors.New(lastLine(out.Stderr))
	}
	if err != nil {
		if oldBlock != nil && oldBlock.Target != "" {
			_, _ = WriteForward(e.Home, e.StateDir, *oldBlock, e.Command, e.now()) // the machine still talks to the old hub
		}
		return ent, err
	}
	return upd, nil
}

// hostPortOf is the host:port of a hub URL (the right side of a RemoteForward).
func hostPortOf(raw string) (string, error) {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return "", exitf(ExitUsage, "%q is not a hub address", raw)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// ListRow is one registry entry with the hub's view.
type ListRow struct {
	Entry
	HubView *HubMachine `json:"hub_view"`
	HubErr  string      `json:"hub_error,omitempty"`
}

// List is `wy machine list` (4.5): the registry, with each machine's hub view when the admin
// channel answers.
func List(ctx context.Context, e *ManageEnv) ([]ListRow, error) {
	reg, err := LoadRegistry(e.StateDir)
	if err != nil {
		return nil, err
	}
	rows := make([]ListRow, 0, len(reg.Machines))
	unsupported := false
	for _, ent := range reg.Machines {
		row := ListRow{Entry: ent}
		if e.Admin != nil && !unsupported && ent.Machine != "" {
			m, err := MachineShow(ctx, e.Admin, ent.Machine)
			var ae *AdminError
			switch {
			case errors.As(err, &ae) && ae.NotImplemented():
				unsupported = true
				row.HubErr = "the hub's wy cannot show machines yet"
			case err != nil:
				row.HubErr = err.Error()
			default:
				row.HubView = &m
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
