package objectstore_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/iampat/cloudy-neigh/objectstore"
)

const crossProcEnv = "OBJECTSTORE_CROSSPROC_DIR"

func TestMain(m *testing.M) {
	if dir := os.Getenv(crossProcEnv); dir != "" {
		os.Exit(crossProcHelper(dir))
	}
	os.Exit(m.Run())
}

func crossProcHelper(dir string) int {
	s, err := objectstore.OpenDisk(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer s.Close()
	_, err = s.PutIfAbsent(context.Background(), "race", strings.NewReader("x"))
	switch {
	case err == nil:
		return 0
	case errors.Is(err, objectstore.ErrPreconditionFailed):
		return 3
	default:
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
}

func TestDiskCrossProcessPutIfAbsent(t *testing.T) {
	dir := t.TempDir() + "/bucket"
	const procs = 4
	done := make(chan int, procs)
	for i := 0; i < procs; i++ {
		go func() {
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), crossProcEnv+"="+dir)
			out, err := cmd.CombinedOutput()
			code := cmd.ProcessState.ExitCode()
			if err != nil && code != 3 {
				t.Logf("helper: %v: %s", err, out)
			}
			done <- code
		}()
	}
	wins, losses := 0, 0
	for i := 0; i < procs; i++ {
		switch <-done {
		case 0:
			wins++
		case 3:
			losses++
		default:
			t.Fatal("helper process failed")
		}
	}
	if wins != 1 || losses != procs-1 {
		t.Fatalf("wins=%d losses=%d, want 1 and %d", wins, losses, procs-1)
	}
}

func TestDiskReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir() + "/bucket"
	s, err := objectstore.OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	gen := put(t, s, "k", "v1")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = objectstore.OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, gen2, err := s.GetWithGeneration(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, r); got != "v1" || gen2 != gen {
		t.Fatalf("after reopen: (%q, %q), want (%q, %q)", got, gen2, "v1", gen)
	}
	if _, err := s.PutIfGenerationMatch(ctx, "k", strings.NewReader("v2"), gen); err != nil {
		t.Fatalf("CAS after reopen: %v", err)
	}
}

func TestDiskListHidesInternals(t *testing.T) {
	ctx := context.Background()
	s, err := objectstore.OpenDisk(t.TempDir() + "/bucket")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	put(t, s, "k", "v")
	objs, err := s.List(ctx, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].Key != "k" {
		t.Fatalf("List = %v, want only %q", objs, "k")
	}
}
