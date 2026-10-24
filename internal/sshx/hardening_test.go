package sshx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mraj602/watchyard/internal/sshx"
	"github.com/mraj602/watchyard/internal/sshx/sshxtest"
)

func TestCheckForbiddenVariants(t *testing.T) {
	bad := [][]string{
		{"-vo", "StrictHostKeyChecking=no"},
		{"-vvo", "StrictHostKeyChecking=no"},
		{"-qoStrictHostKeyChecking=no"},
		{"-Tv", "-o", "UserKnownHostsFile=/dev//null"},
		{"-o", "UserKnownHostsFile=/dev/./null"},
		{"-o", "UserKnownHostsFile=//dev/null"},
		{"-o", "UserKnownHostsFile=\"/dev/null\""},
		{"-o", "GlobalKnownHostsFile", "/dev/../dev/null"},
		{"-o", "StrictHostKeyChecking='no'"},
	}
	for _, w := range bad {
		if err := sshx.CheckForbidden(w); !errors.Is(err, sshx.ErrForbidden) {
			t.Errorf("%q: %v, want ErrForbidden", w, err)
		}
	}
	good := [][]string{
		{"-v"},
		{"-vo", "Port=2222"},
		{"-i", "key"},
		{"-F", "/dev/null"},
		{"-o", "UserKnownHostsFile=/dev/nullx"},
	}
	for _, w := range good {
		if err := sshx.CheckForbidden(w); err != nil {
			t.Errorf("%q refused: %v", w, err)
		}
	}
}

func TestAutoDefaultsAndBatch(t *testing.T) {
	f := sshxtest.New(t)
	f.RemoteHome("gpu-box")
	r, err := sshx.FromEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	r.AutoDefaults, r.Batch = true, true
	for i := 0; i < 2; i++ {
		if _, err := r.Run(context.Background(), sshx.Call{Host: "gpu-box", Remote: []string{"true"}}); err != nil {
			t.Fatal(err)
		}
	}
	var runs, gs int
	for _, c := range f.Calls() {
		if c.G {
			gs++
			continue
		}
		runs++
		for _, want := range []string{"ConnectTimeout=15", "ServerAliveInterval=15", "BatchMode=yes"} {
			if !contains(c.Argv, want) {
				t.Errorf("call %q lacks %s", c.Argv, want)
			}
		}
	}
	if runs != 2 || gs != 1 {
		t.Fatalf("runs %d, ssh -G %d (want 2 and 1: looked up once per host)", runs, gs)
	}
}
