// Package update implements `wy update` and the release download shared with
// `wy machine add`: install.sh in Go (docs/spec/machines.md 7.3). No GitHub API: releases are
// found through SHA256SUMS, every archive is verified against it, and nothing downloaded is
// run before verification.
//
// This file holds the types and the Fetcher seam that `wy machine add` (which needs the
// archive for the remote's os/arch) and `wy update` share. Resolve, Fetch and Extract are in
// fetch.go; tests fake them through Fetcher.
package update

import (
	"context"
	"net/http"

	"github.com/mraj602/watchyard/internal/brand"
)

// DefaultBaseURL is the release source when neither WY_INSTALL_BASE_URL nor update.base_url
// is set (machines.md 7.3 step 1).
const DefaultBaseURL = "https://github.com/mraj602/watchyard/releases"

// Latest is the version argument that means the newest release.
const Latest = "latest"

// Source says where releases come from.
type Source struct {
	// BaseURL: WY_INSTALL_BASE_URL, else update.base_url, else DefaultBaseURL. https only
	// (http for a loopback host, tests).
	BaseURL string
	// HTTP is the client to use; nil means one with sane timeouts that follows redirects
	// only to https.
	HTTP *http.Client
}

// SumLine is one line of SHA256SUMS: the SHA-256 (lower-case hex) of a release file.
type SumLine struct {
	SHA256 string
	Name   string // e.g. wy_0.5.0_linux_arm64.tar.gz
}

// Release is a resolved release: a version and its parsed SHA256SUMS.
type Release struct {
	Version string // without the leading v: "0.5.0"
	Base    string // the Source.BaseURL it was resolved from
	// SumsURL is where SHA256SUMS was fetched from; archives are next to it.
	SumsURL string
	// Sums is SHA256SUMS in file order. SumsRaw is the file as downloaded, so it can be
	// pushed to a remote that has no internet (`wy update --sums-file`, machines.md 7.4).
	Sums    []SumLine
	SumsRaw []byte

	http *http.Client // the client Resolve used, so Fetch follows the same redirect rules
}

// ArchiveName is the release archive name for a platform: wy_<v>_<os>_<arch>.tar.gz, .zip on
// Windows (machines.md 7.3 step 2).
func (r Release) ArchiveName(goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return brand.CLI + "_" + r.Version + "_" + goos + "_" + goarch + ext
}

// Archive is a downloaded release archive whose SHA-256 matched its SHA256SUMS line. Nothing
// here has been run or extracted.
type Archive struct {
	Release Release
	GOOS    string
	GOARCH  string
	Name    string // file name
	Path    string // file in the cache folder
	SHA256  string // lower-case hex of the archive, equal to the SHA256SUMS line
}

// Fetcher is the seam callers code against and tests fake: the three calls above as an
// interface. Real is the package-level implementation.
type Fetcher interface {
	Resolve(ctx context.Context, src Source, version string) (Release, error)
	Fetch(ctx context.Context, rel Release, goos, goarch, cacheDir string) (Archive, error)
	Extract(a Archive) (binPath, sha256 string, err error)
}

// Real is the Fetcher backed by this package's functions.
type Real struct{}

func (Real) Resolve(ctx context.Context, src Source, version string) (Release, error) {
	return Resolve(ctx, src, version)
}

func (Real) Fetch(ctx context.Context, rel Release, goos, goarch, cacheDir string) (Archive, error) {
	return Fetch(ctx, rel, goos, goarch, cacheDir)
}

func (Real) Extract(a Archive) (string, string, error) { return Extract(a) }
