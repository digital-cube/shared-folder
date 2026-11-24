package client_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/transport"
	"github.com/digital-cube/shared-folder/internal/transport/rsyncssh"
)

// refusing is the local transport, or, while refuse is set, a server
// whose sshd refuses the key (as rsyncssh reports it).
type refusing struct {
	transport.Transport
	refuse atomic.Bool
	calls  atomic.Int32 // attempts while refusing
}

func (r *refusing) err() error {
	r.calls.Add(1)
	return fmt.Errorf("rsync tmp/x: %w: ana@127.0.0.1: Permission denied (publickey).", rsyncssh.ErrAuth)
}

func (r *refusing) Send(ctx context.Context, u transport.Upload, src string) error {
	if r.refuse.Load() {
		return r.err()
	}
	return r.Transport.Send(ctx, u, src)
}

func (r *refusing) Fetch(ctx context.Context, shareID, p, hash, dst string) error {
	if r.refuse.Load() {
		return r.err()
	}
	return r.Transport.Fetch(ctx, shareID, p, hash, dst)
}

// TestKeyRefused: a refused key ends the cycle at the first transfer
// (each further one would be another refused login), the error says what
// to do, and the folder's state remembers it until a transfer goes
// through again; joining again clears it.
func TestKeyRefused(t *testing.T) {
	e := newEnv(t)
	tr := &refusing{Transport: transport.Local{DataRoot: e.root}}
	e.tune = func(o *client.Options) { o.Transport = tr }
	ana := e.joined("ana")
	ctx := context.Background()

	tr.refuse.Store(true)
	for i := range 5 {
		ana.write(fmt.Sprintf("f%d.txt", i), "x")
	}
	_, err := ana.c.Sync(ctx)
	if !errors.Is(err, client.ErrKeyRefused) || !errors.Is(err, rsyncssh.ErrAuth) || client.Classify(err) != client.FailKeyRefused {
		t.Fatalf("sync: %v", err)
	}
	if !strings.Contains(err.Error(), "run shf join again") {
		t.Errorf("the error does not say what to do: %v", err)
	}
	if n := tr.calls.Load(); n != 1 {
		t.Errorf("%d transfer attempts after the key was refused, want 1", n)
	}
	st, err := ana.c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	since := st.KeyRefusedSince
	if since.IsZero() {
		t.Fatal("status: key refused not recorded")
	}

	// nothing to transfer: the cycle succeeds, but says nothing about the key
	for i := range 5 {
		ana.remove(fmt.Sprintf("f%d.txt", i))
	}
	ana.sync()
	if st, _ = ana.c.Status(ctx); !st.KeyRefusedSince.Equal(since) {
		t.Errorf("a cycle without transfers changed the key state: %v", st.KeyRefusedSince)
	}

	// a transfer goes through: the server accepts the key again
	tr.refuse.Store(false)
	ana.write("ok.txt", "ok")
	ana.sync()
	if st, _ = ana.c.Status(ctx); !st.KeyRefusedSince.IsZero() {
		t.Errorf("key still marked refused after a transfer: %v", st.KeyRefusedSince)
	}

	// refused again; joining again at the same folder registers the key
	tr.refuse.Store(true)
	ana.write("again.txt", "x")
	if _, err := ana.c.Sync(ctx); client.Classify(err) != client.FailKeyRefused {
		t.Fatalf("sync: %v", err)
	}
	ana.c.Close()
	tr.refuse.Store(false)
	sh, err := e.srv.Share(e.share)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := sh.ResetToken("ana") // the member's own token, as an admin hands it out again
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := client.Join(ctx, client.JoinRequest{Home: ana.home, URL: e.url, ShareID: e.share, Token: tok, Folder: ana.dir}, e.options())
	if err != nil {
		t.Fatalf("joining again: %v", err)
	}
	ana.c = c
	t.Cleanup(func() { c.Close() })
	if st, _ = ana.c.Status(ctx); !st.KeyRefusedSince.IsZero() {
		t.Errorf("key still marked refused after joining again: %v", st.KeyRefusedSince)
	}
	ana.mustRead("again.txt", "x")
	if got, _ := e.storage("again.txt"); got != "x" {
		t.Errorf("server: again.txt = %q after joining again", got)
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		err  error
		want client.Failure
	}{
		{nil, client.FailOther},
		{errors.New("boom"), client.FailOther},
		{fmt.Errorf("put a: %w: x", rsyncssh.ErrAuth), client.FailKeyRefused},
		{errors.Join(errors.New("a"), fmt.Errorf("get b: %w", rsyncssh.ErrUnreachable)), client.FailUnreachable},
		{&client.APIError{Status: 500, Code: "internal"}, client.FailOther},
		{context.Canceled, client.FailOther},
	} {
		if got := client.Classify(c.err); got != c.want {
			t.Errorf("Classify(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
