-- Version 13: session_state.peers (states.md 3.1, push.md 3.5): the current turn is coordination
-- between sessions ('message': a peer message started it; 'wait': it ended waiting on another
-- session's reply). NULL for every row folded before; the next event of a session sets it.

ALTER TABLE session_state ADD COLUMN peers TEXT CHECK (peers IS NULL OR peers IN ('message', 'wait'));
