// Package identity finds config folders (slots), reads the login (seat) of each from
// .claude.json oauthAccount or `claude auth status --json`, and creates and stores the
// machine id. It never reads credentials or tokens.
//
// Contract: docs/spec/identity.md.
//
// Depends on: atomicfile.
package identity
