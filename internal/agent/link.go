package agent

import "context"

// LinkClient is the link client (link.md, internal/linkclient): the agent starts it in its
// own goroutine and reports its status; the agent itself has no network code. With none, the
// agent is a same-machine service.
type LinkClient interface {
	// Run blocks until ctx is done. An error is logged; the agent keeps serving local sends.
	Run(ctx context.Context) error
	// Status is shown by `wy agent status`.
	Status() LinkStatus
}

// LinkStatus is the link's state line: State is a short word ("none", "connected",
// "reconnecting", "stopped"), Detail the reason when there is one.
type LinkStatus struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}
