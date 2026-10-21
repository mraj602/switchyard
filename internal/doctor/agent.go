package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/mraj602/switchyard/internal/agent"
	"github.com/mraj602/switchyard/internal/client"
	"github.com/mraj602/switchyard/internal/config"
	"github.com/mraj602/switchyard/internal/keys"
)

// checkAgent reports the peers agent (peers.md 2) when peers are enabled. A machine with
// peers on and no agent still works on the direct same-machine path, so a missing agent is a
// warning, not a failure. It only connects to the agent's own control socket.
func checkAgent(rep *Report, o Options, cfg config.Config) {
	if !cfg.Peers.Enabled || runtime.GOOS == "windows" {
		return
	}
	xdg := ""
	if o.Getenv != nil {
		xdg = o.Getenv("XDG_RUNTIME_DIR")
	}
	sock := agent.SocketPath(runtime.GOOS, xdg, o.Dirs.State.Path)
	rep2, err := agent.Call(sock, agent.Request{Op: agent.OpState}, 2*time.Second)
	switch {
	case err == nil && rep2.State != nil:
		rep.add(Check{Name: "agent", Level: OK, Detail: fmt.Sprintf("running (pid %d), link: %s", rep2.State.PID, rep2.State.Link.State)})
	case err == nil:
		rep.add(Check{Name: "agent", Level: Warn, Detail: "answers on its socket but gave no state"})
	default:
		rep.add(Check{Name: "agent", Level: Warn, Detail: "peers are enabled but the sy agent is not running (same-machine sends write the inbox directly)",
			Fix: "sy agent enable"})
	}
	checkLinkKeys(rep, o, cfg)
}

// CertInfo is what the hub holds of this machine's link keys (GET /v1/devices/self/cert).
type CertInfo struct {
	Device, Reg, SignFP string
	OwnerSigned         bool
}

// ErrNoCert is a CertProbe answer: the hub has no keys for this machine.
var ErrNoCert = errors.New("the hub has no link keys for this machine")

// checkLinkKeys reports this machine's link keys when peers are on and it has a route to a
// hub (keys.md 4.2, peers.md 2.1): none at all, none at the hub, or not owner-certified
// (peers on other machines refuse an uncertified sender). A hub that does not answer is left
// to the flush and machine checks.
func checkLinkKeys(rep *Report, o Options, cfg config.Config) {
	if cfg.Hub.URL == "" || cfg.Hub.Reach == config.ReachPull {
		return
	}
	dk, err := keys.ReadDeviceKeys(o.Dirs.State.Path)
	if err != nil {
		rep.add(Check{Name: "link keys", Level: Warn, Detail: "this machine has no link keys: it cannot hold a link, so peers on other machines cannot reach it",
			Fix: "sy agent enable"})
		return
	}
	pub, err := dk.Public()
	if err != nil {
		rep.add(Check{Name: "link keys", Level: Warn, Detail: err.Error(), Fix: "sy agent enable"})
		return
	}
	probe := o.CertProbe
	if probe == nil {
		probe = func(ctx context.Context) (CertInfo, error) { return realCertProbe(ctx, o, cfg) }
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ci, err := probe(ctx)
	sp, _ := keys.UnB64(pub.Sign.Pub)
	signFP := keys.Fingerprint(sp)
	_, perr := keys.ReadPin(keys.Path(o.Dirs.State.Path, keys.OwnerPinFile))
	ownerPinned := perr == nil
	switch {
	case errors.Is(err, ErrNoCert):
		rep.add(Check{Name: "link keys", Level: Warn, Detail: "this machine has link keys, but the hub has none registered for it", Fix: "sy agent enable"})
	case err != nil:
		return
	case ci.SignFP != signFP:
		rep.add(Check{Name: "link keys", Level: Warn, Detail: fmt.Sprintf("the hub holds other link keys for this machine (sign %s, here %s)", keys.GroupFP(ci.SignFP), keys.GroupFP(signFP)),
			Fix: "sy agent enable (or sy machine add again from the owner machine)"})
	case !ci.OwnerSigned && !ownerPinned:
		// No owner key pinned here: most likely none was ever made, so certify alone would fail.
		rep.add(Check{Name: "link keys", Level: Warn, Detail: fmt.Sprintf("not owner-certified (reg %s, sign %s) and no owner key pinned here: peers on other machines refuse this machine, and it cannot check theirs", ci.Reg, keys.GroupFP(signFP)),
			Fix: "on the owner machine, in a terminal: sy keys init (once, if sy keys status there shows no owner key), confirm it, then sy keys certify --all; then sy agent enable on each other machine"})
	case !ci.OwnerSigned:
		rep.add(Check{Name: "link keys", Level: Warn, Detail: fmt.Sprintf("not owner-certified (reg %s, sign %s): peers on other machines refuse this machine", ci.Reg, keys.GroupFP(signFP)),
			Fix: "on the owner machine: sy keys certify " + ci.Device})
	case !ownerPinned:
		rep.add(Check{Name: "link keys", Level: Warn, Detail: fmt.Sprintf("owner-certified (reg %s, sign %s), but no owner key pinned here: this machine cannot check other machines' certificates, so its messages to them are refused", ci.Reg, keys.GroupFP(signFP)),
			Fix: "sy agent enable (pins the hub's confirmed owner key)"})
	default:
		rep.add(Check{Name: "link keys", Level: OK, Detail: fmt.Sprintf("owner-certified (reg %s, sign %s)", ci.Reg, keys.GroupFP(signFP))})
	}
}

func realCertProbe(ctx context.Context, o Options, cfg config.Config) (CertInfo, error) {
	tok, err := client.ReadToken(o.Dirs.State.Path)
	if err != nil {
		return CertInfo{}, err
	}
	c, err := client.ForConfig(cfg, tok)
	if err != nil {
		return CertInfo{}, err
	}
	doc, signed, err := c.DeviceCert(ctx, "self")
	var se *client.StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return CertInfo{}, ErrNoCert
	}
	if err != nil {
		return CertInfo{}, err
	}
	// Read only (this is a report): the body as the hub sent it.
	raw, err := doc.BodyBytes()
	if err != nil {
		return CertInfo{}, err
	}
	var b keys.CertBody
	if err := json.Unmarshal(raw, &b); err != nil {
		return CertInfo{}, err
	}
	return CertInfo{Device: b.Device, Reg: b.Reg, SignFP: b.SignFP(), OwnerSigned: signed}, nil
}
