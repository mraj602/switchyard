package store

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/switchyard/internal/brand"
)

// rename.md 2.6: an export made before a rename (kind <old>-hub-export) is still read.
func TestUnpackReadsLegacyKind(t *testing.T) {
	defer brand.SetLegacyForTest("oldname", "Oldname")()
	write := func(kind string) string {
		b, _ := json.Marshal(Manifest{Format: ManifestFormat, Kind: kind, Schema: 1})
		path := filepath.Join(t.TempDir(), "x.syx")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		zw := gzip.NewWriter(f)
		tw := tar.NewWriter(zw)
		if err := tw.WriteHeader(&tar.Header{Name: memberManifest, Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(b)
		tw.Close()
		zw.Close()
		f.Close()
		return path
	}
	const refused = "not a hub export this sy reads"
	for kind, want := range map[string]bool{ManifestKind: true, "oldname-hub-export": true, "other-hub-export": false, "": false} {
		_, err := unpack(write(kind), "", t.TempDir())
		if got := err == nil || !strings.Contains(err.Error(), refused); got != want {
			t.Errorf("kind %q: read %v, want %v (%v)", kind, got, want, err)
		}
	}
}
