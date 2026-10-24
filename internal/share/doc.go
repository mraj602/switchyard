// Package share is the file-system side of sharing between Claude config folders
// (setup.md 5.1, 5.3, 5.4, 5.7): the item table, link/junction/copy primitives that never
// leave a path absent (temp name + rename), tree comparison, the conflict merges (history
// line union, directory union that is renames only), promotion, the MCP three-way merge of
// the `mcpServers` key through internal/jsonedit, Windows synced copies, and Sync for
// `wy tick`.
//
// Sharer implements model.Sharer, so internal/setup/apply drives it through the interface.
// Nothing here ever reads or writes `.credentials.json` or any `.claude.json` key but
// `mcpServers`; log lines and results carry server names, never values.
//
// Depends on: internal/setup/model, internal/jsonedit,
// internal/atomicfile, internal/identity (the Forbidden guard).
package share
