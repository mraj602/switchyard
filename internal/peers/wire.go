package peers

import "github.com/mraj602/switchyard/internal/keys"

// The peer frames on the link (link.md 6.2-6.4), shared by the hub's relay and the agent.

// PeerMsgUp is the device→hub `peer_msg`: the signed envelope.
type PeerMsgUp struct {
	T   string   `json:"t"`
	Doc keys.Doc `json:"doc"`
}

// PeerAck is the hub's ephemeral answer to a peer_msg. Detail is the reason word of a
// refusal, or "offline" for a message queued while the target machine is not connected.
type PeerAck struct {
	V      int    `json:"v"`
	Msg    string `json:"msg"`
	Status string `json:"status"` // queued | refused
	Detail string `json:"detail,omitempty"`
	// Exp is when the queued message expires (unix seconds).
	Exp int64 `json:"exp,omitempty"`
}

// PeerResultUp is the receiving agent's verdict (link.md 6.2 peer_result).
type PeerResultUp struct {
	T       string `json:"t"`
	Msg     string `json:"msg"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	HumanAt string `json:"human_at,omitempty"`
}

// PeerLogUp is a same-machine delivery's metadata (link.md 6.2 peer_log).
type PeerLogUp struct {
	T           string `json:"t"`
	Msg         string `json:"msg"`
	Thread      string `json:"thread"`
	FromSession string `json:"from_session"`
	ToSession   string `json:"to_session"`
	Kind        Kind   `json:"kind"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	Size        int    `json:"size"`
	At          string `json:"at"`
}

// PeersListUp asks for the session directory.
type PeersListUp struct {
	T  string `json:"t"`
	ID string `json:"id"`
}

// WireSession is one session of the hub's directory (peers.md 3.2, without `can`, which the
// asking agent works out with its own matrix and threads).
type WireSession struct {
	Session      string  `json:"session"`
	Handle       string  `json:"handle"`
	Name         string  `json:"name"`
	Account      string  `json:"account"`
	AccountLabel string  `json:"account_label"`
	Machine      string  `json:"machine"` // the machine's name
	MachineID    string  `json:"machine_id"`
	Project      string  `json:"project"`
	State        string  `json:"state"`
	Device       *string `json:"device"`
}

// PeersReply is the hub's `peers` answer: other machines' sessions and the certificates of
// their agents' devices.
type PeersReply struct {
	V        int                 `json:"v"`
	ID       string              `json:"id"`
	Sessions []WireSession       `json:"sessions"`
	Certs    map[string]keys.Doc `json:"certs"`
}

// QueuedPeerMsg is the body of a queued `peer_msg` (link.md 6.4).
type QueuedPeerMsg struct {
	V    int      `json:"v"`
	Doc  keys.Doc `json:"doc"`
	Cert keys.Doc `json:"cert"`
}

// PeerStatusBody is the body of a queued `peer_status`: a sent message's final state.
type PeerStatusBody struct {
	V      int    `json:"v"`
	Msg    string `json:"msg"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	At     string `json:"at"`
}
