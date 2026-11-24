package daemon_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/password"
	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/transport"
)

// TestSignedOutFolderStops: a folder joined by account whose device is
// revoked on the server stops after one refused cycle, logged once, with
// no retries; a new sign-in (its credentials change on disk) starts it
// again.
func TestSignedOutFolderStops(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := password.Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	srv, err := server.Open(root, server.Options{PasswordParams: &p})
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(srv, nil)
	hs := httptest.NewServer(api)
	t.Cleanup(func() { api.Close(); hs.Close(); srv.Close() })
	w := &world{t: t, root: root, srv: srv, url: hs.URL}
	if w.share, _, err = srv.CreateShare("tim", "admin"); err != nil {
		t.Fatal(err)
	}
	u, temp, err := srv.AddUser("bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.AddUserMember(w.shareObj(), "bob", false); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	opts := client.Options{Transport: transport.Local{DataRoot: root}}
	pr := &peer{t: t, w: w, user: "bob", home: t.TempDir(), dir: filepath.Join(t.TempDir(), "bob"), url: hs.URL}
	signIn := func(pw string) *client.Account {
		t.Helper()
		a, mustChange, err := client.SignIn(ctx, client.SignInRequest{URL: hs.URL, Username: "bob", Password: pw, DeviceName: "mac"})
		if err != nil {
			t.Fatal(err)
		}
		if mustChange {
			if err := a.ChangePassword(ctx, nil, pw, "bobova-nova-lozinka"); err != nil {
				t.Fatal(err)
			}
		}
		if err := client.SaveAccount(pr.home, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	acct := signIn(temp)
	c, _, err := client.Join(ctx, client.JoinRequest{Home: pr.home, URL: hs.URL, ShareID: "tim", Account: acct, Folder: pr.dir}, opts)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	set := fast()
	set.Poll = client.Duration(200 * time.Millisecond) // the daemon looks for changed credentials this often
	pr.start(set)
	pr.write("a.txt", "a")
	waitFor(t, 10*time.Second, "a.txt on the server", func() bool { got, _ := w.storage("a.txt"); return got == "a" })

	if err := srv.RevokeDevice(u.ID, acct.DeviceID); err != nil {
		t.Fatal(err)
	}
	pr.write("b.txt", "b")
	signedOut := func() int {
		pr.mu.Lock()
		defer pr.mu.Unlock()
		return strings.Count(pr.log.String(), `level=ERROR msg="signed out: this device is signed out of `+hs.URL+": run shf login "+hs.URL)
	}
	waitFor(t, 10*time.Second, "the signed-out folder logged", func() bool { return signedOut() == 1 })
	n := len(pr.cyclesOf(""))
	time.Sleep(1500 * time.Millisecond) // several debounces and rescans
	pr.write("c.txt", "c")
	time.Sleep(700 * time.Millisecond)
	if got := len(pr.cyclesOf("")); got != n || signedOut() != 1 {
		t.Errorf("a signed-out folder went on: %d cycles after %d, logged %d times", got, n, signedOut())
	}
	if _, ok := w.storage("b.txt"); ok {
		t.Error("b.txt reached the server from a revoked device")
	}

	// shf login: a new device, the folder gets its credentials
	acct = signIn("bobova-nova-lozinka")
	plan, err := client.PlanLinks(ctx, pr.home, acct, mustMe(t, acct), nil)
	if err != nil || len(plan.Again) != 1 {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if err := client.Relink(ctx, plan.Again[0].Config, acct, opts); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "b.txt and c.txt on the server after signing in again", func() bool {
		b, _ := w.storage("b.txt")
		c, _ := w.storage("c.txt")
		return b == "b" && c == "c"
	})
}

func mustMe(t *testing.T, a *client.Account) *client.Me {
	t.Helper()
	me, err := a.Me(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return me
}
