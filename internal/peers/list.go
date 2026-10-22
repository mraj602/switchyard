package peers

// List is the session list for the caller StartPID reaches (peers.md 3.2). Inside a session
// it is what that session may reach (all is ignored there: `--all` is terminal only, 3.2);
// at a terminal only all is allowed. asked says the list is from a session's point of view.
// With a hub, its answer comes first, so this machine's sessions show their canonical
// handles; when the hub cannot be asked the list shows this machine only.
func (e Env) List(all bool) (entries []ListEntry, asked bool, rf *Refusal) {
	caller, rf := e.Whoami()
	switch {
	case rf == nil:
	case rf.Code == CodeNotASession && all:
		// A terminal: every session, no asking session.
	case rf.Code == CodeNotASession:
		return nil, false, Refuse(CodeNotASession, "sy peers list works inside a Claude Code session; at a terminal use --all")
	default:
		return nil, false, rf
	}
	dir, everyone, _ := e.directoryAll()
	var from *Session
	if rf == nil {
		for i := range dir {
			if dir[i].Entry.SessionID == caller.Entry.SessionID {
				from = &dir[i]
			}
		}
	}
	return e.Entries(everyone, from, from == nil), from != nil, nil
}
