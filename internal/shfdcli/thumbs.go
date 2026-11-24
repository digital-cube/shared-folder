package shfdcli

import (
	"bytes"
	"context"
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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
	"github.com/digital-cube/shared-folder/internal/server/httpapi"
	"github.com/digital-cube/shared-folder/internal/thumbs"
	"github.com/digital-cube/shared-folder/internal/version"
)

// shfd thumbs makes the thumbnails of every folder of a data-root, in a
// process of its own: decoding other people's files costs CPU and memory
// and may crash, and none of that may touch shfd serve, through which the
// sync runs. It never opens a database (one writer per folder; SQLite on
// NFS hung the server once): it asks the running shfd serve for jobs over
// admin.sock, reads storage/ and writes only thumbnails/
// (docs/admin.md, "Thumbnails").

// ThumbsLock is the file, in the db-root, that keeps a second shfd thumbs
// off the same data-root.
const ThumbsLock = "thumbs.lock"

// Timings of shfd thumbs; tests shorten them.
var (
	thumbsIdle         = 2 * time.Second // between polls when there is nothing to do
	thumbsCleanupEvery = 24 * time.Hour  // removal of thumbnails nothing holds
)

// thumbsStaleTemp is how old a temporary file in thumbnails/ must be
// before the cleanup removes it.
const thumbsStaleTemp = time.Hour

func thumbsCmd(args []string, env Env) error {
	fs := flag.NewFlagSet("thumbs", flag.ContinueOnError)
	workers := fs.Int("workers", 1, "thumbnails made at the same time")
	pdftoppm := fs.String("pdftoppm", "", "pdftoppm of poppler-utils, for PDFs (default $"+thumbs.EnvPDFToPPM+", else pdftoppm on PATH)")
	once := fs.Bool("once", false, "make what waits and exit (exit 1 when shfd serve does not answer)")
	retry := fs.Bool("retry-failed", false, "remove the .fail markers first, so those files are tried again")
	maxImage := fs.Int64("max-image-mib", thumbs.DefaultMaxImageBytes>>20, "largest image file, in MiB")
	maxPixels := fs.Int64("max-megapixels", thumbs.DefaultMaxPixels/1_000_000, "largest image, in megapixels (width × height)")
	maxPDF := fs.Int64("max-pdf-mib", thumbs.DefaultMaxPDFBytes>>20, "largest PDF file, in MiB")
	pdfTimeout := fs.Duration("pdf-timeout", thumbs.DefaultPDFTimeout, "time pdftoppm gets for one PDF")
	root, _, err := flags(fs, args, 0, env)
	if err != nil {
		return err
	}
	if *workers < 1 || *maxImage < 1 || *maxPixels < 1 || *maxPDF < 1 || *pdfTimeout <= 0 {
		return usageError("thumbs: --workers, the limits and --pdf-timeout must be positive")
	}
	data, db, err := root.abs()
	if err != nil {
		return err
	}
	for _, d := range []string{data, db} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", d)
		}
	}
	unlock, err := lockFile(filepath.Join(db, ThumbsLock), "another shfd thumbs runs on "+db)
	if err != nil {
		return err
	}
	defer unlock()
	ctx := env.Ctx
	if ctx == nil {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	svc := &thumbsService{
		data:    data,
		client:  newThumbsClient(db),
		workers: *workers,
		log:     log,
		gen: &thumbs.Generator{Limits: thumbs.Limits{
			MaxImageBytes: *maxImage << 20, MaxPixels: *maxPixels * 1_000_000,
			MaxPDFBytes: *maxPDF << 20, PDFTimeout: *pdfTimeout,
		}},
		kinds: []thumbs.Kind{thumbs.KindImage},
	}
	defer svc.client.close()
	if p, err := thumbs.FindPDFToPPM(*pdftoppm, env.Getenv(thumbs.EnvPDFToPPM)); err != nil {
		log.Warn("pdftoppm not found: PDFs get no thumbnails, images do", "err", err, "hint", "apt install poppler-utils, or give --pdftoppm or $"+thumbs.EnvPDFToPPM)
	} else {
		svc.gen.PDFToPPM = p
		svc.kinds = append(svc.kinds, thumbs.KindPDF)
	}
	log.Info("shfd thumbs starting", "data_root", data, "db_root", db, "workers", *workers,
		"kinds", kindsParam(svc.kinds), "pdftoppm", svc.gen.PDFToPPM, "version", version.Version)
	if err := svc.prepare(*retry); err != nil {
		return err
	}
	return svc.run(ctx, *once)
}

type thumbsService struct {
	data    string
	client  *thumbsClient
	gen     *thumbs.Generator
	kinds   []thumbs.Kind
	workers int
	log     *slog.Logger
}

// shareDirs are the folders of the data-root, by their directories.
func (s *thumbsService) shareDirs() ([]string, error) {
	des, err := os.ReadDir(s.data)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, de := range des {
		if de.IsDir() && server.ValidShareID(de.Name()) {
			out = append(out, de.Name())
		}
	}
	return out, nil
}

func (s *thumbsService) store(share string) thumbs.Store {
	return thumbs.NewStore(filepath.Join(s.data, share))
}

// prepare turns the .work markers of a previous run that died into .fail
// markers and, with retry, removes every .fail marker.
func (s *thumbsService) prepare(retry bool) error {
	shares, err := s.shareDirs()
	if err != nil {
		return err
	}
	for _, id := range shares {
		st := s.store(id)
		crashed, err := st.RecoverWork()
		if err != nil {
			return fmt.Errorf("folder %s: %w", id, err)
		}
		for _, h := range crashed {
			s.log.Warn("a thumbnail was being made when shfd thumbs stopped; it is marked failed", "share", id, "hash", h, "hint", "shfd thumbs --retry-failed tries it again")
		}
		if retry {
			n, err := st.ClearFailed()
			if err != nil {
				return fmt.Errorf("folder %s: %w", id, err)
			}
			if n > 0 {
				s.log.Info("failed thumbnails cleared for another try", "share", id, "count", n)
			}
		}
	}
	return nil
}

// run asks for jobs and does them until ctx ends (or, with once, until
// none are left).
func (s *thumbsService) run(ctx context.Context, once bool) error {
	rescan := true // the server must read the markers prepare changed
	var lastCleanup time.Time
	connected := -1 // unknown; 0 down, 1 up: one log line per change
	for ctx.Err() == nil {
		j, err := s.client.jobs(ctx, s.kinds, rescan)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if once {
				return fmt.Errorf("shfd serve does not answer on its admin socket (%v); thumbs needs it running", err)
			}
			if connected != 0 {
				s.log.Warn("shfd serve does not answer on its admin socket; waiting for it", "err", err)
				connected = 0
			}
			sleepCtx(ctx, thumbsIdle)
			continue
		}
		if connected != 1 {
			s.log.Info("connected to shfd serve", "pending", j.Pending)
			connected = 1
		}
		rescan = false
		if time.Since(lastCleanup) >= thumbsCleanupEvery {
			s.cleanup(ctx)
			lastCleanup = time.Now()
		}
		if len(j.Jobs) == 0 {
			if once {
				break
			}
			sleepCtx(ctx, thumbsIdle)
			continue
		}
		if !s.batch(ctx, j.Jobs) {
			// The store failed (disk full?) or the server went away:
			// the same jobs come back, so do not ask at once.
			sleepCtx(ctx, thumbsIdle)
		}
	}
	s.log.Info("shfd thumbs stopped")
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// batch does jobs with the workers; false when any of them could not be
// done or reported.
func (s *thumbsService) batch(ctx context.Context, jobs []server.ThumbJob) bool {
	ch := make(chan server.ThumbJob)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := true
	for range min(s.workers, len(jobs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				if !s.job(ctx, j) {
					mu.Lock()
					ok = false
					mu.Unlock()
				}
			}
		}()
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		ch <- j
	}
	close(ch)
	wg.Wait()
	return ok
}

// job makes one thumbnail and reports it. A thumbnail that exists already
// (the server went down before the last report) is only reported.
func (s *thumbsService) job(ctx context.Context, j server.ThumbJob) bool {
	start := time.Now()
	if !server.ValidShareID(j.ShareID) || !thumbs.ValidHash(j.Hash) {
		s.log.Error("invalid thumbnail job", "share", j.ShareID, "hash", j.Hash)
		return false
	}
	st := s.store(j.ShareID)
	var res thumbs.Result
	switch {
	case st.Ready(j.Hash):
		res.Outcome = thumbs.Ready
	case st.Failed(j.Hash):
		res.Outcome = thumbs.Failed
		res.Reason, _ = st.FailReason(j.Hash)
	default:
		var err error
		res, err = s.make(ctx, st, j)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("thumbnail not made", "share", j.ShareID, "hash", j.Hash, "kind", j.Kind, "err", err)
			}
			return false
		}
	}
	attrs := []any{"share", j.ShareID, "hash", j.Hash, "kind", j.Kind, "took", time.Since(start).Round(time.Millisecond), "outcome", res.Outcome}
	if res.Reason != "" {
		attrs = append(attrs, "reason", res.Reason)
	}
	s.log.Info("thumbnail", attrs...)
	s.log.Debug("thumbnail source", "share", j.ShareID, "hash", j.Hash, "path", j.Path)
	r := server.ThumbResult{Hash: j.Hash, OK: res.Outcome == thumbs.Ready, Skipped: res.Outcome == thumbs.Skipped}
	if res.Outcome == thumbs.Failed {
		r.Error = res.Reason
	}
	if err := s.client.done(ctx, j.ShareID, r); err != nil {
		if ctx.Err() == nil {
			s.log.Warn("thumbnail outcome not reported; the job comes back and is only reported then", "share", j.ShareID, "hash", j.Hash, "err", err)
		}
		return false
	}
	return true
}

// make opens the source inside storage/ (symlinks may not lead out of
// it) and generates. A source that is gone or not what the job says is
// skipped.
func (s *thumbsService) make(ctx context.Context, st thumbs.Store, j server.ThumbJob) (thumbs.Result, error) {
	if j.Kind == thumbs.KindPDF && s.gen.PDFToPPM == "" {
		return thumbs.Result{Outcome: thumbs.Skipped, Reason: "no pdftoppm"}, nil
	}
	f, err := os.OpenInRoot(filepath.Join(s.data, j.ShareID, server.StorageDir), filepath.FromSlash(j.Path))
	if err != nil {
		return thumbs.Result{Outcome: thumbs.Skipped, Reason: "source not readable"}, nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() != j.Size {
		return thumbs.Result{Outcome: thumbs.Skipped, Reason: "source is not the job's file"}, nil
	}
	return s.gen.Generate(ctx, st, f, j.Hash, j.Kind)
}

// cleanup removes the thumbnails no file and no trash entry that is not
// purged hold any more, asking the server in batches of
// server.MaxThumbHashes; and temporary files an interrupted write left.
func (s *thumbsService) cleanup(ctx context.Context) {
	start := time.Now()
	shares, err := s.shareDirs()
	if err != nil {
		s.log.Error("thumbnail cleanup", "err", err)
		return
	}
	removed := 0
	for _, id := range shares {
		st := s.store(id)
		if _, err := st.RemoveStaleTemp(thumbsStaleTemp, time.Now()); err != nil {
			s.log.Warn("thumbnail cleanup: temporary files", "share", id, "err", err)
		}
		hs, err := st.Hashes()
		if err != nil {
			s.log.Error("thumbnail cleanup", "share", id, "err", err)
			continue
		}
		for len(hs) > 0 && ctx.Err() == nil {
			n := min(len(hs), server.MaxThumbHashes)
			un, err := s.client.unreferenced(ctx, id, hs[:n])
			if err != nil {
				s.log.Warn("thumbnail cleanup: folder skipped", "share", id, "err", err)
				break
			}
			for _, h := range un {
				if err := st.Remove(h); err != nil {
					s.log.Warn("thumbnail cleanup: not removed", "share", id, "hash", h, "err", err)
					continue
				}
				removed++
			}
			hs = hs[n:]
		}
	}
	s.log.Info("thumbnail cleanup", "removed", removed, "took", time.Since(start).Round(time.Millisecond))
}

func kindsParam(ks []thumbs.Kind) string {
	ss := make([]string, len(ks))
	for i, k := range ks {
		ss[i] = string(k)
	}
	return strings.Join(ss, ",")
}

// thumbsClient talks to the thumbnail routes of shfd serve on admin.sock.
// The socket path is resolved at every connection, so a restarted server
// is found again.
type thumbsClient struct {
	hc *http.Client
	tr *http.Transport
}

func newThumbsClient(dbAbs string) *thumbsClient {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			sock, err := adminSocketPath(dbAbs, false)
			if err != nil {
				return nil, err
			}
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		MaxIdleConnsPerHost: 4,
	}
	return &thumbsClient{hc: &http.Client{Transport: tr, Timeout: time.Minute}, tr: tr}
}

func (c *thumbsClient) close() { c.tr.CloseIdleConnections() }

func (c *thumbsClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://shfd"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
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
		if resp.StatusCode == http.StatusNotFound {
			return errors.New("shfd serve has no thumbnail routes; is it an older version?")
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

func (c *thumbsClient) jobs(ctx context.Context, kinds []thumbs.Kind, rescan bool) (*server.ThumbJobs, error) {
	q := url.Values{"limit": {strconv.Itoa(httpapi.DefaultThumbJobs)}, "kinds": {kindsParam(kinds)}}
	if rescan {
		q.Set("rescan", "1")
	}
	var j server.ThumbJobs
	if err := c.do(ctx, "GET", "/v1/thumbs/jobs?"+q.Encode(), nil, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

func (c *thumbsClient) done(ctx context.Context, share string, rs ...server.ThumbResult) error {
	var out map[string]string
	return c.do(ctx, "POST", "/v1/thumbs/done", httpapi.ThumbsDoneRequest{ShareID: share, Results: rs}, &out)
}

func (c *thumbsClient) unreferenced(ctx context.Context, share string, hashes []string) ([]string, error) {
	var out httpapi.HashesBody
	if err := c.do(ctx, "POST", "/v1/shares/"+url.PathEscape(share)+"/admin/thumbs/unreferenced", httpapi.HashesBody{Hashes: hashes}, &out); err != nil {
		return nil, err
	}
	return out.Hashes, nil
}
