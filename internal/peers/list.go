package peers

// List is the session list for the caller StartPID reaches (peers.md 3.2). Inside a session
// it is what that session may reach (all: every session, with what the matrix allows);
// at a terminal only all is allowed. asked says the list is from a session's point of view.
func (e Env) List(all bool) (entries []ListEntry, asked bool, rf *Refusal) {
	dir := e.Directory()
	var from *Session
	caller, rf := e.Whoami()
	switch {
	case rf == nil:
		for i := range dir {
			if dir[i].Entry.SessionID == caller.Entry.SessionID {
				from = &dir[i]
			}
		}
	case rf.Code == CodeNotASession && all:
		// A terminal: every session, no asking session.
	case rf.Code == CodeNotASession:
		return nil, false, Refuse(CodeNotASession, "sy peers list works inside a Claude Code session; at a terminal use --all")
	default:
		return nil, false, rf
	}
	return e.Entries(dir, from, all || from == nil), from != nil, nil
}
