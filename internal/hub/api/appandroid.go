package api

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/mraj602/switchyard/internal/hub/appmirror"
	"github.com/mraj602/switchyard/internal/hub/auth"
)

// AppMirror is the hub's mirror of the newest Android release (internal/hub/appmirror, phone.md
// P16.2). Nil: both routes answer 404 "nothing mirrored".
type AppMirror interface {
	Current() (appmirror.Release, bool)
}

func init() {
	Register("GET", "/v1/app/android", func(s *Server) http.Handler { return http.HandlerFunc(s.androidRelease) })
	Register("GET", "/v1/app/android/apk", func(s *Server) http.Handler { return http.HandlerFunc(s.androidAPK) })
}

func (s *Server) mirrored() (appmirror.Release, bool) {
	if s.cfg.AppMirror == nil {
		return appmirror.Release{}, false
	}
	return s.cfg.AppMirror.Current()
}

// androidRelease serves the manifest as released, plus the hub's download url.
func (s *Server) androidRelease(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.mirrored()
	if !ok {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no app release is mirrored")
		return
	}
	var m map[string]any
	if json.Unmarshal(rel.Raw, &m) != nil {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no app release is mirrored")
		return
	}
	m["url"] = "/v1/app/android/apk"
	writeJSON(w, http.StatusOK, m)
}

// androidAPK streams the APK unchanged (Range works through ServeContent).
func (s *Server) androidAPK(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.mirrored()
	if !ok {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no app release is mirrored")
		return
	}
	f, err := os.Open(rel.APKPath)
	if err != nil {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no app release is mirrored")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("ETag", `"`+rel.Manifest.SHA256+`"`)
	http.ServeContent(w, r, rel.Manifest.APK, time.Time{}, f)
}
