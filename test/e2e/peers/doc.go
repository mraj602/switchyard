// Package peers is the PE2 end-to-end test of peer messages across machines
// (docs/spec/peers.md 15, PE2): two machines, each a temp HOME, state folder, runtime folder
// and fake /proc with Claude Code sessions played by the fake inbox, each running the real
// agent (control socket, peers pipeline, peerlink, linkclient), and one real hub in process
// (store, keys, link, peer relay) on a loopback httptest server.
//
//	go test -tags fakeinbox ./test/e2e/peers     (part of `make test`)
//
// Nothing here touches the real HOME, $XDG_RUNTIME_DIR, cc-socks, credentials or session
// key files: every path is under a temp folder and the sessions are fakes. Without the
// fakeinbox tag the package has no tests.
package peers
