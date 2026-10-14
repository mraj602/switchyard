// Package agent is `sy agent run`: the one per-machine user service of peers.md 2. It owns the
// control socket (2.3), the same-machine delivery behind it (peers.Env.Send) and a 1-minute
// tick, and it is where the link client (link.md) will run: see LinkClient. `sy hook` never
// talks to it, and `sy peers` falls back to writing the inbox itself when it is not running.
package agent
