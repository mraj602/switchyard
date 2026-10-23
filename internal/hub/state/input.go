package state

import (
	"encoding/json"
	"fmt"
	"time"
)

// Input is one fact about one session, in the form the fold needs. Exactly one of Hook,
// List and Transcript is set. It deliberately does not depend on internal/record; a later step adapts a
// record to an Input (hook_event -> HookInput, session_list -> a ListObs per session).
type Input struct {
	ID   string // record id; tie-break for equal timestamps
	TS   time.Time
	Hook *Hook
	List *ListObs
	// Transcript: the session's transcript has activity at TS (token_rows, states.md 5.1).
	// It is no live signal: it only gives a session that no hook event or list ever saw
	// the state ended:history, ended at its newest transcript activity.
	Transcript *TranscriptObs
}

// TranscriptObs is "the transcript was written at Input.TS" (the newest row of the session
// in a token_rows record). TS may be zero when the transcript gave no time.
type TranscriptObs struct{}

// Hook is the part of a hook_event the fold reads.
type Hook struct {
	Name             string // hook_event_name
	Source           string // SessionStart: startup, resume, clear, compact
	Reason           string // SessionEnd
	Error            string // StopFailure
	NotificationType string // Notification
	ToolName         string // PreToolUse, PostToolUse, ...
	StopHookActive   bool
	// LastMessage is the Stop/StopFailure message tail (record derived.last_message). Valid
	// only when HasLastMessage; when it is absent the detector never fires.
	LastMessage    string
	HasLastMessage bool
	// Question is derived.question of an AskUserQuestion PreToolUse, if known.
	Question string
	// PeerPrompt: derived.peer_prompt of a UserPromptSubmit, a turn a peer message started
	// (record.md 4.1). PeerWait: derived.peer_wait of a Stop, the session waits on another
	// session's reply (peers.md 6.4).
	PeerPrompt, PeerWait bool
}

// PeerFacts sets PeerPrompt and PeerWait from a hook_event record's derived fields; anything
// but a JSON true leaves them false.
func (h *Hook) PeerFacts(derived map[string]json.RawMessage) {
	h.PeerPrompt = string(derived["peer_prompt"]) == "true"
	h.PeerWait = string(derived["peer_wait"]) == "true"
}

// ListObs is what one session_list says about one session (the caller picks the session and
// only passes lists of the session's own slot).
type ListObs struct {
	Complete bool   // the list is every live session of the slot
	Listed   bool   // the session is in it
	Status   string // busy, idle, waiting, or "" (when Listed)
	// LastActive is the session's newest sign of life the hub has at or before the list's
	// ts, apart from lists: its newest transcript row, else the start the list reports (zero
	// when neither is known). A list that makes the session done backdates state_since to
	// it, so a session first seen idle by a list (an import, or a slot without hooks) is not
	// "done since the list ran" (states.md 4).
	LastActive time.Time
}

type hookPayload struct {
	Name             string  `json:"hook_event_name"`
	Source           string  `json:"source"`
	Reason           string  `json:"reason"`
	Error            string  `json:"error"`
	NotificationType string  `json:"notification_type"`
	ToolName         string  `json:"tool_name"`
	StopHookActive   bool    `json:"stop_hook_active"`
	LastAssistantMsg *string `json:"last_assistant_message"`
}

// HookInput decodes a hook payload (the JSON Claude Code wrote to the hook's stdin, as
// stored in a hook_event record) into an Input. lastMessage is the record's
// derived.last_message (nil when absent). When it is nil, payload.last_assistant_message is
// used if present, so raw captured payloads (testdata/hooks) fold too.
func HookInput(id string, ts time.Time, payload []byte, lastMessage *string) (Input, error) {
	var p hookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return Input{}, fmt.Errorf("state: decode hook payload: %w", err)
	}
	h := &Hook{
		Name: p.Name, Source: p.Source, Reason: p.Reason, Error: p.Error,
		NotificationType: p.NotificationType, ToolName: p.ToolName, StopHookActive: p.StopHookActive,
	}
	if lastMessage != nil {
		h.LastMessage, h.HasLastMessage = *lastMessage, true
	} else if p.LastAssistantMsg != nil {
		h.LastMessage, h.HasLastMessage = *p.LastAssistantMsg, true
	}
	return Input{ID: id, TS: ts, Hook: h}, nil
}
