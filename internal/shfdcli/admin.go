package shfdcli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
)

// AdminSocket is the file name, inside the data-root, of the unix socket on
// which shfd serve answers the admin routes for the shfd commands.
const AdminSocket = "admin.sock"

// maxSocketPath keeps socket paths under the limit of sockaddr_un (104
// bytes on macOS, 108 on Linux).
const maxSocketPath = 100

// adminSocketPath is <data-root>/admin.sock, or, when that path is too
// long for a unix socket, <tmp>/shfd-<uid>/<hash of data-root>.sock in a
// directory only this user may enter. With create the directory is made.
func adminSocketPath(absRoot string, create bool) (string, error) {
	p := filepath.Join(absRoot, AdminSocket)
	if len(p) <= maxSocketPath {
		return p, nil
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("shfd-%d", os.Getuid()))
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	if err := checkPrivateDir(dir); err != nil && (create || !errors.Is(err, os.ErrNotExist)) {
		return "", err
	}
	sum := sha256.Sum256([]byte(absRoot))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".sock"), nil
}

// adminConn reaches the admin routes of a data-root: through the socket of
// a running shfd serve, so the command goes through that server's single
// writer per folder; or, when no server runs, in this process while it
// holds the data-root's lock, so none can start meanwhile.
type adminConn struct {
	hc    *http.Client
	as    string
	close func()
}

func dialAdmin(root, as string) (*adminConn, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	sock, err := adminSocketPath(abs, false)
	if err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", sock, 2*time.Second); err == nil {
		c.Close()
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
		return &adminConn{hc: &http.Client{Transport: tr}, as: as, close: tr.CloseIdleConnections}, nil
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("data-root %s: %w", abs, err)
	}
	unlock, err := lockDataRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("%w, but its admin socket %s does not answer; is it an older shfd? Restart it, or stop it and run the command again", err, sock)
	}
	srv, err := server.Open(abs, server.Options{})
	if err != nil {
		unlock()
		return nil, err
	}
	h := httpapi.NewLocal(srv, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &adminConn{
		hc: &http.Client{Transport: handlerTransport{h}},
		as: as,
		close: func() {
			srv.Close()
			unlock()
		},
	}, nil
}

// call sends one request to an admin route of a folder and decodes the
// answer into out. An error answer becomes a *server.Error.
func (c *adminConn) call(method, shareID, path string, query url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	u := "http://shfd/v1/shares/" + url.PathEscape(shareID) + "/admin/" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.as != "" {
		req.Header.Set(httpapi.HeaderAs, c.as)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var eb httpapi.ErrorBody
		if json.Unmarshal(raw, &eb) == nil && eb.Error.Code != "" {
			return &server.Error{Code: eb.Error.Code, Message: eb.Error.Message}
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

// handlerTransport runs requests through an http.Handler in this process.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := &recorder{header: http.Header{}, code: http.StatusOK}
	if req.Body == nil {
		req.Body = http.NoBody
	}
	t.h.ServeHTTP(rec, req)
	return &http.Response{
		StatusCode: rec.code, Status: http.StatusText(rec.code),
		Header: rec.header, Body: io.NopCloser(&rec.body), Request: req,
		ContentLength: int64(rec.body.Len()),
	}, nil
}

type recorder struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(p []byte) (int, error) { return r.body.Write(p) }
func (r *recorder) WriteHeader(code int)        { r.code = code }

// serveAdminSocket answers the admin routes on <data-root>/admin.sock
// until the returned stop is called. The caller holds the data-root lock,
// so a socket file left there is stale and is replaced.
func serveAdminSocket(srv *server.Server, log *slog.Logger) (func(), error) {
	sock, err := adminSocketPath(srv.Root(), true)
	if err != nil {
		return nil, err
	}
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("admin socket %s: %w", sock, err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	hs := &http.Server{Handler: httpapi.NewLocal(srv, log), ReadHeaderTimeout: 10 * time.Second}
	go hs.Serve(ln)
	return func() {
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
		os.Remove(sock)
	}, nil
}

// adminFlags parses the flags every admin command shares and connects.
func adminFlags(fs *flag.FlagSet, args []string, nPos int, env Env) (*adminConn, []string, error) {
	as := fs.String("as", "", "admin in whose name the command runs (default: the folder's oldest active admin)")
	root, pos, err := flags(fs, args, nPos, env)
	if err != nil {
		return nil, nil, err
	}
	if !server.ValidShareID(pos[0]) {
		return nil, nil, usageError("%s: %q is not a share id", fs.Name(), pos[0])
	}
	c, err := dialAdmin(root, *as)
	if err != nil {
		return nil, nil, err
	}
	return c, pos, nil
}

func trashCmd(args []string, env Env) error {
	if len(args) == 0 {
		return usageError("trash: want list, restore or purge")
	}
	switch args[0] {
	case "list":
		return trashListCmd(args[1:], env)
	case "restore":
		return trashRestoreCmd(args[1:], env)
	case "purge":
		return trashPurgeCmd(args[1:], env)
	}
	return usageError("trash: unknown subcommand %q", args[0])
}

func trashListCmd(args []string, env Env) error {
	fs := flag.NewFlagSet("trash list", flag.ContinueOnError)
	prefix := fs.String("path", "", "only entries whose original path starts with this")
	by := fs.String("by", "", "only entries sent to the trash by this member")
	reason := fs.String("reason", "", "deleted or replaced")
	since := fs.String("since", "", "only entries from this date (YYYY-MM-DD or RFC 3339) on")
	c, pos, err := adminFlags(fs, args, 1, env)
	if err != nil {
		return err
	}
	defer c.close()
	q := url.Values{}
	for k, v := range map[string]string{"path": *prefix, "by": *by, "reason": *reason, "since": *since} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var l server.TrashList
	if err := c.call("GET", pos[0], "trash", q, nil, &l); err != nil {
		return err
	}
	printTrash(env.Stdout, l.Entries)
	fmt.Fprintf(env.Stdout, "%d %s, %s\n", l.Count, plural(l.Count, "entry", "entries"), fmtSize(l.TotalSize))
	return nil
}

func printTrash(w io.Writer, es []server.TrashEntry) {
	if len(es) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPATH\tREASON\tBY\tAT\tSIZE")
	for _, e := range es {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\n", e.ID, e.OrigPath, e.Reason, e.By, e.At.UTC().Format(time.RFC3339), e.Size)
	}
	tw.Flush()
}

func trashRestoreCmd(args []string, env Env) error {
	fs := flag.NewFlagSet("trash restore", flag.ContinueOnError)
	to := fs.String("to", "", "restore to this path instead of the original one")
	c, pos, err := adminFlags(fs, args, 2, env)
	if err != nil {
		return err
	}
	defer c.close()
	id, err := strconv.ParseInt(pos[1], 10, 64)
	if err != nil || id <= 0 {
		return usageError("trash restore: %q is not a trash id", pos[1])
	}
	var res server.RestoreResult
	if err := c.call("POST", pos[0], "trash/"+strconv.FormatInt(id, 10)+"/restore", nil, httpapi.RestoreRequest{To: *to}, &res); err != nil {
		return err
	}
	switch {
	case res.Status == server.StatusUnchanged:
		fmt.Fprintf(env.Stdout, "%s already has the content of trash entry %d; nothing changed\n", res.Path, id)
	case res.Renamed:
		want := *to
		if want == "" {
			want = "the original path"
		}
		fmt.Fprintf(env.Stdout, "restored trash entry %d as %s (seq %d): %s holds other content and was left alone\n", id, res.Path, res.Head, want)
	default:
		fmt.Fprintf(env.Stdout, "restored trash entry %d to %s (seq %d)\n", id, res.Path, res.Head)
	}
	return nil
}

// idList is a repeatable --id.
type idList []int64

func (l *idList) String() string { return fmt.Sprint(*l) }
func (l *idList) Set(s string) error {
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("%q is not a trash id", f)
		}
		*l = append(*l, n)
	}
	return nil
}

func trashPurgeCmd(args []string, env Env) error {
	fs := flag.NewFlagSet("trash purge", flag.ContinueOnError)
	older := fs.String("older-than", "", "entries older than this age (36h, 30d, 2w)")
	var ids idList
	fs.Var(&ids, "id", "this trash entry (repeatable, or comma-separated)")
	all := fs.Bool("all", false, "every entry")
	dry := fs.Bool("dry-run", false, "only list what would be purged")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	c, pos, err := adminFlags(fs, args, 1, env)
	if err != nil {
		return err
	}
	defer c.close()
	n := 0
	for _, set := range []bool{*older != "", len(ids) > 0, *all} {
		if set {
			n++
		}
	}
	if n != 1 {
		return usageError("trash purge: choose exactly one of --older-than, --id and --all")
	}
	req := server.PurgeRequest{OlderThan: *older, IDs: ids, All: *all, DryRun: *dry || !*yes}
	var res server.PurgeResult
	if err := c.call("POST", pos[0], "trash/purge", nil, req, &res); err != nil {
		return err
	}
	if res.DryRun {
		printTrash(env.Stdout, res.Entries)
		if res.Count == 0 {
			fmt.Fprintln(env.Stdout, "nothing to purge")
			return nil
		}
		if *dry {
			fmt.Fprintf(env.Stdout, "would purge %d %s, %s (dry run, nothing changed)\n", res.Count, plural(res.Count, "entry", "entries"), fmtSize(res.TotalSize))
			return nil
		}
		fmt.Fprintf(env.Stdout, "Purge %d %s, %s, for good? This cannot be undone. [y/N] ", res.Count, plural(res.Count, "entry", "entries"), fmtSize(res.TotalSize))
		in := env.Stdin
		if in == nil {
			in = os.Stdin
		}
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("not confirmed; nothing purged (use --yes to skip the question)")
		}
		// Exactly what was shown, even if the trash grew meanwhile.
		req = server.PurgeRequest{IDs: make([]int64, 0, len(res.Entries))}
		for _, e := range res.Entries {
			req.IDs = append(req.IDs, e.ID)
		}
		if err := c.call("POST", pos[0], "trash/purge", nil, req, &res); err != nil {
			return err
		}
	}
	if res.Count == 0 {
		fmt.Fprintln(env.Stdout, "nothing to purge")
		return nil
	}
	fmt.Fprintf(env.Stdout, "purged %d %s, %s\n", res.Count, plural(res.Count, "entry", "entries"), fmtSize(res.TotalSize))
	return nil
}

func verifyCmd(args []string, env Env) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	repair := fs.Bool("repair", false, "bring storage, trash and tmp in line with the database (through the change log)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	c, pos, err := adminFlags(fs, args, 1, env)
	if err != nil {
		return err
	}
	defer c.close()
	var res server.VerifyResult
	if err := c.call("POST", pos[0], "verify", nil, httpapi.VerifyRequest{Repair: *repair}, &res); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(res)
	} else {
		printVerify(env.Stdout, &res)
	}
	switch left := res.Unrepaired(); {
	case left == 0:
		return nil
	case *repair:
		return fmt.Errorf("%d %s could not be repaired", left, plural(left, "issue", "issues"))
	default:
		return fmt.Errorf("%d %s found; shfd verify --repair fixes them", left, plural(left, "issue", "issues"))
	}
}

func printVerify(w io.Writer, res *server.VerifyResult) {
	ck := res.Checked
	fmt.Fprintf(w, "checked %d %s, %d %s, %d trash %s, %d tmp %s\n",
		ck.Files, plural(ck.Files, "file", "files"), ck.Dirs, plural(ck.Dirs, "directory", "directories"),
		ck.Trash, plural(ck.Trash, "entry", "entries"), ck.Tmp, plural(ck.Tmp, "entry", "entries"))
	if len(res.Issues) == 0 {
		fmt.Fprintln(w, "no issues: storage/ and trash/ match the database")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	hdr := "AREA\tKIND\tPATH\tDETAIL"
	if res.Repair {
		hdr += "\tREPAIR"
	}
	fmt.Fprintln(tw, hdr)
	for _, is := range res.Issues {
		line := fmt.Sprintf("%s\t%s\t%s\t%s", is.Area, is.Kind, is.Path, is.Detail)
		if res.Repair {
			switch {
			case is.Repaired && is.Seq > 0:
				line += fmt.Sprintf("\trepaired (seq %d)", is.Seq)
			case is.Repaired:
				line += "\trepaired"
			default:
				line += "\tnot repaired"
			}
		}
		fmt.Fprintln(tw, line)
	}
	tw.Flush()
	for _, is := range res.Issues {
		if is.Warning != "" {
			fmt.Fprintf(w, "warning: %s %s: %s\n", is.Area, is.Path, is.Warning)
		}
	}
	n, rep := len(res.Issues), len(res.Issues)-res.Unrepaired()
	if res.Repair {
		fmt.Fprintf(w, "%d %s, %d repaired\n", n, plural(n, "issue", "issues"), rep)
	} else {
		fmt.Fprintf(w, "%d %s\n", n, plural(n, "issue", "issues"))
	}
}

func statsCmd(args []string, env Env) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the statistics as JSON")
	c, pos, err := adminFlags(fs, args, 1, env)
	if err != nil {
		return err
	}
	defer c.close()
	var st server.Stats
	if err := c.call("GET", pos[0], "stats", nil, nil, &st); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	w := env.Stdout
	fmt.Fprintf(w, "share-id:  %s\n", st.ShareID)
	fmt.Fprintf(w, "head:      %d\n", st.Head)
	fmt.Fprintf(w, "storage:   %d %s, %s; %d %s\n", st.Files, plural(int(st.Files), "file", "files"), fmtSize(st.FilesSize), st.Dirs, plural(int(st.Dirs), "directory", "directories"))
	fmt.Fprintf(w, "trash:     %d %s, %s", st.Trash, plural(int(st.Trash), "entry", "entries"), fmtSize(st.TrashSize))
	if st.Purged > 0 {
		fmt.Fprintf(w, "; %d purged", st.Purged)
	}
	fmt.Fprintf(w, "\nuploads:   %d open\n\n", st.OpenUploads)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "USERNAME\tROLE\tSTATUS\tLAST SEEN\tLAST PUSH\tACKED SEQ\tBEHIND")
	for _, m := range st.Members {
		status := "active"
		if m.Revoked {
			status = "revoked " + when(m.RevokedAt)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%d\n", m.Username, m.Role, status, when(m.LastSeenAt), when(m.LastPushAt), m.LastSeqAcked, m.Behind)
	}
	return tw.Flush()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// fmtSize is "1.5 MiB (1572864 bytes)", or "N bytes" below 1 KiB.
func fmtSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d %s", n, plural(int(n), "byte", "bytes"))
	}
	v, unit := float64(n), ""
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB"} {
		v /= 1024
		unit = u
		if v < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s (%d bytes)", v, unit, n)
}
