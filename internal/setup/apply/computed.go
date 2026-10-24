package apply

import (
	"os"
	"path/filepath"

	"github.com/mraj602/switchyard/internal/atomicfile"
	"github.com/mraj602/switchyard/internal/brand"
	"github.com/mraj602/switchyard/internal/setup/backup"
	"github.com/mraj602/switchyard/internal/setup/model"
)

// marketplaceName is sy's local marketplace ("sy-local", plan.MarketplaceName): the only
// name under the plugin roots whose new paths undo of a `run` removes.
const marketplaceName = brand.Marketplace

// warpMarketplaceName is Warp's marketplace (plan.WarpMarketplace).
const warpMarketplaceName = "claude-code-warp"

// MCPBasePath is <state>/share/mcp-base.json, the MCP sync's base (share.MCPBasePath).
func MCPBasePath(stateDir string) string { return filepath.Join(stateDir, "share", "mcp-base.json") }

// mergeLines merges a.From into a.Path (history.jsonl). The merge is computed on a scratch
// copy inside the set first, so the entry records the exact bytes it will write and recovery
// of an interrupted run can tell them from later appends.
func (r *runner) mergeLines(a model.Action) error {
	sh := r.d.Sharer
	fi, err := os.Lstat(a.Path)
	if err != nil || !fi.Mode().IsRegular() {
		// not a plain file (a link, or missing): merge in place, nothing known up front
		spec := backup.Spec{Paths: []backup.PathSpec{
			{Path: a.Path, Save: backup.SaveCopy}, {Path: a.From, Save: backup.SaveCopy}}}
		return r.journaled(a, spec, func(*backup.Tx) error { return sh.MergeLines(a.Path, a.From) })
	}
	tmp, err := r.set.Scratch()
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	old, err := os.ReadFile(a.Path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, old, 0o600); err != nil {
		return err
	}
	if err := sh.MergeLines(tmp, a.From); err != nil {
		return err
	}
	merged, err := os.ReadFile(tmp)
	if err != nil {
		return err
	}
	mode := fi.Mode().Perm()
	fromSt, err := backup.Capture(a.From)
	if err != nil {
		return err
	}
	spec := backup.Spec{Paths: []backup.PathSpec{
		{Path: a.Path, Save: backup.SaveCopy, Intended: fileState(merged, mode)},
		{Path: a.From, Save: backup.SaveCopy, Intended: &fromSt}}}
	return r.journaled(a, spec, func(*backup.Tx) error { return atomicfile.Write(a.Path, merged, mode) })
}

// writeTree writes the marketplace tree into a scratch folder of the set, records its tree
// hash as the intended state, then moves any existing tree into the set and the new one
// into place.
func (r *runner) writeTree(a model.Action) error {
	tmp, err := r.set.Scratch()
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := r.d.Plugins.WriteMarketplace(tmp, r.o.SyPath, r.o.SyVersion); err != nil {
		return err
	}
	h, err := backup.TreeHash(tmp)
	if err != nil {
		return err
	}
	spec := backup.Spec{Paths: append(ancestors(a.Path), backup.PathSpec{Path: a.Path, Save: backup.SaveReserve,
		Intended: &model.PathState{Type: model.TypeDir, SHA256: h}})}
	return r.journaled(a, spec, func(tx *backup.Tx) error {
		if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
			return err
		}
		if to := tx.SaveTo(a.Path); to != "" {
			if err := backup.MoveTree(a.Path, to); err != nil {
				return err
			}
		}
		return backup.MoveTree(tmp, a.Path)
	})
}
