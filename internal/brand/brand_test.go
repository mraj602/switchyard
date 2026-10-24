package brand

import "testing"

func TestNames(t *testing.T) {
	for _, s := range []string{Slug, LegacySlug} {
		if s == "" {
			t.Fatal("empty slug")
		}
		for _, c := range s {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
				t.Fatalf("slug %q must be [a-z0-9]", s)
			}
		}
	}
	if Name == "" || LegacyName == "" {
		t.Fatal("empty name")
	}
	if OldSlug() != LegacySlug || OldName() != LegacyName {
		t.Fatal("the legacy names in effect must be the constants outside tests")
	}
}

func TestNotRenamed(t *testing.T) {
	defer SetLegacyForTest(Slug, Name)()
	if Renamed() || Legacy(Slug+"-hub") != "" || Current(Slug+"-hub") != Slug+"-hub" {
		t.Fatal("not renamed: Legacy must be empty and Current the identity")
	}
}

func TestLegacyAndCurrent(t *testing.T) {
	defer SetLegacyForTest("oldname", "Oldname")()
	if !Renamed() {
		t.Fatal("Renamed")
	}
	for in, want := range map[string]string{Slug + "-hub": "oldname-hub", Name + " Hub": "Oldname Hub", "other": ""} {
		if got := Legacy(in); got != want {
			t.Errorf("Legacy(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"oldname-hub": Slug + "-hub", "Oldname Hub": Name + " Hub", "other": "other", Slug: Slug} {
		if got := Current(in); got != want {
			t.Errorf("Current(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMigrateEnv(t *testing.T) {
	env := []string{"OLD_HOME=/h", "OLD_BIN=/b", EnvPrefix + "BIN=/new", "PATH=/usr/bin"}
	got := map[string]string{}
	set := func(k, v string) error { got[k] = v; return nil }
	if s := MigrateEnv(env, set); s != nil {
		t.Fatalf("not renamed: %v", s)
	}
	defer SetLegacyCLIForTest("old")()
	MigrateEnv(env, set)
	if len(got) != 1 || got[EnvPrefix+"HOME"] != "/h" {
		t.Fatalf("got %v", got)
	}
}
