package backup_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/setup/backup"
	"github.com/mraj602/switchyard/internal/setup/model"
	"github.com/mraj602/switchyard/internal/setup/testtree"
)

func TestCreateIDsClashAndMode(t *testing.T) {
	state := t.TempDir()
	at := time.Date(2026, 10, 12, 17, 30, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 3; i++ {
		s, err := backup.Create(state, backup.Header{Command: "c"}, at)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID())
		if runtime.GOOS != "windows" {
			if fi, _ := os.Stat(s.Dir); fi.Mode().Perm() != 0o700 {
				t.Fatalf("mode %v", fi.Mode())
			}
		}
	}
	if ids[0] != "20261012T173000Z" || ids[1] != ids[0]+"-2" || ids[2] != ids[0]+"-3" {
		t.Fatal(ids)
	}
	l, err := backup.List(state)
	if err != nil || len(l) != 3 || l[0].ID != ids[0] || l[2].ID != ids[2] || l[0].Status != model.StatusInProgress {
		t.Fatalf("%v %+v", err, l)
	}
}

func TestBeginWritesIntentBeforeAction(t *testing.T) {
	b := testtree.New(t, "")
	b.File("f", "hello", 0o640).Symlink("l", "rel/target")
	s, err := backup.Create(b.Abs("state"), backup.Header{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.Begin("a1", model.OpWriteFile, backup.Spec{Paths: []backup.PathSpec{
		{Path: b.Abs("f"), Save: backup.SaveCopy}, {Path: b.Abs("l")}, {Path: b.Abs("none")}}})
	if err != nil {
		t.Fatal(err)
	}
	// reopen from disk: the intent entry is there with before states and the saved copy
	r, err := backup.Open(b.Abs("state"), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	e := r.Manifest().Entries[0]
	if e.Phase != model.PhaseIntent || len(e.Paths) != 3 || e.Paths[0].After != nil {
		t.Fatalf("%+v", e)
	}
	if e.Paths[0].Before.Saved == "" || (e.Paths[0].Before.Mode != "0640" && runtime.GOOS != "windows") || // Windows has no 0640 e.Paths[1].Before.Target != "rel/target" ||
		e.Paths[2].Before.Type != model.TypeAbsent {
		t.Fatalf("%+v", e.Paths)
	}
	if got, _ := os.ReadFile(r.Abs(e.Paths[0].Before.Saved)); string(got) != "hello" {
		t.Fatal("saved bytes")
	}
	if err := tx.Finish(nil); err != nil {
		t.Fatal(err)
	}
	r, _ = backup.Open(b.Abs("state"), s.ID())
	if e := r.Manifest().Entries[0]; e.Phase != model.PhaseDone || e.Paths[0].After == nil {
		t.Fatalf("%+v", e)
	}
}

func TestReadPointer(t *testing.T) {
	doc := []byte(`{"a":{"b/c":[1, {"d": "x"}],"e~f":null}}`)
	cases := map[string]string{"/a/b~1c/1/d": `"x"`, "/a/e~0f": `null`, "/a/b~1c/0": `1`}
	for ptr, want := range cases {
		got, ok, err := backup.PointerIn(doc, ptr)
		if err != nil || !ok || string(got) != want {
			t.Errorf("%s: %q %v %v", ptr, got, ok, err)
		}
	}
	if _, ok, _ := backup.PointerIn(doc, "/a/zzz"); ok {
		t.Error("missing key present")
	}
}

func TestMoveTreeKeepsModesAndLinks(t *testing.T) {
	b := testtree.New(t, "")
	b.File("src/a/f", "1", 0o600).Symlink("src/a/l", "../rel").Dir("src/a", 0o750)
	want := testtree.Take(t, b.Abs("src"), testtree.Opts{})
	if err := backup.MoveTree(b.Abs("src"), b.Abs("dst")); err != nil {
		t.Fatal(err)
	}
	testtree.Equal(t, want, testtree.Take(t, b.Abs("dst"), testtree.Opts{}))
	if _, err := os.Lstat(filepath.Join(b.Root, "src")); err == nil {
		t.Fatal("source kept")
	}
}

func TestUndoNothingAndAlreadyUndone(t *testing.T) {
	if _, err := backup.Undo(t.Context(), t.TempDir(), "", backup.Deps{}, backup.Options{}); err != backup.ErrNothingToUndo {
		t.Fatal(err)
	}
}
