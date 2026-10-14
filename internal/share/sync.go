package share

import (
	"context"

	"github.com/mraj602/switchyard/internal/setup/model"
)

// SyncConfig is what `sy tick` hands to Sync.
type SyncConfig struct {
	// MCP is the sync request (the files of the MCP sync set); nil when MCP sharing is off
	// or "external" (setup.md 5.4).
	MCP *model.MCPSyncRequest
	// Copies runs the Windows synced-copy pass (copies.json); harmless when there are none.
	Copies bool
	// CopyPaths are the copies the ledger lists (model.Ledger.Copies): only they are synced.
	CopyPaths []string
}

// SyncResult is what a tick did.
type SyncResult struct {
	MCP    model.MCPSyncResult
	Copies CopySyncResult
}

// Idle is true when the pass wrote nothing.
func (r SyncResult) Idle() bool {
	return len(r.MCP.Written) == 0 && len(r.Copies.Synced) == 0
}

// Sync is the share part of `sy tick`: the MCP merge and the synced copies. When nothing
// changed it writes nothing (no state file, no base, no log line).
func (s *Sharer) Sync(ctx context.Context, cfg SyncConfig) (SyncResult, error) {
	var out SyncResult
	if cfg.MCP != nil {
		r, err := s.MCPSync(ctx, *cfg.MCP)
		out.MCP = r
		if err != nil {
			return out, err
		}
	}
	if cfg.Copies && s.StateDir != "" {
		r, err := SyncCopies(s.StateDir, cfg.CopyPaths)
		out.Copies = r
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
