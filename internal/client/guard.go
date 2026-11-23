package client

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// ---- mass-delete guard (docs/specifikacija.md, 8.4) -----------------------

// MassDelete are the limits of the mass-delete guard. A cycle that would
// send the server more file deletions than the smaller of the two limits
// sends none of them (nor the directory removals that go with them); the
// folder is "blocked" until shf confirm or shf restore-local.
type MassDelete struct {
	// Files: more than this many file deletions block. Negative: no limit
	// by count.
	Files int `json:"files"`
	// Percent: more deletions than this share of the folder's synchronized
	// files block. Negative: no limit by share.
	Percent float64 `json:"percent"`
}

// DefaultMassDelete: more than 20 files or more than 10 % of the folder,
// whichever is less.
var DefaultMassDelete = MassDelete{Files: 20, Percent: 10}

// Allowed is how many file deletions one cycle may send in a folder of n
// files; ok is false when neither limit applies.
func (g MassDelete) Allowed(n int) (allowed int, ok bool) {
	allowed = -1
	if g.Files >= 0 {
		allowed = g.Files
	}
	if g.Percent >= 0 {
		p := int(math.Floor(float64(n) * g.Percent / 100))
		if allowed < 0 || p < allowed {
			allowed = p
		}
	}
	return allowed, allowed >= 0
}

// HeldDelete is a deletion the guard holds back.
type HeldDelete struct {
	Path string
	Kind Kind // File: a delete; Dir: a rmdir
	// BaseHash is the version of the file that was deleted here.
	BaseHash string
	// Confirmed: shf confirm released it; the next cycle sends it.
	Confirmed bool
	Since     time.Time
}

func isDeletion(a Action) bool { return a.Op == OpDelete || a.Op == OpRmdir }

// guard applies the mass-delete guard to a plan. While deletions are held
// (the folder is blocked) every new deletion joins them, whatever its
// number; deletions released by shf confirm pass. When none of the held
// deletions is planned any more (the files came back), the block ends.
func (cs *cycleState) guard(acts []Action, manifest map[string]ManifestEntry) ([]Action, error) {
	st := cs.c.st
	held, err := st.HeldDeletes()
	if err != nil {
		return nil, err
	}
	prev := map[string]HeldDelete{}
	blocked := false
	for _, h := range held {
		prev[h.Path] = h
		blocked = blocked || !h.Confirmed
	}
	var keep []HeldDelete // confirmed deletions still planned
	var dels []int        // deletions subject to the guard
	files := 0
	for i, a := range acts {
		if !isDeletion(a) {
			continue
		}
		if h, ok := prev[a.Path]; ok && h.Confirmed {
			keep = append(keep, h)
			continue
		}
		dels = append(dels, i)
		if a.Op == OpDelete {
			files++
		}
	}
	n := 0
	for _, me := range manifest {
		if me.State == Synced && me.Kind == File {
			n++
		}
	}
	allowed, limited := cs.c.massDelete().Allowed(n)
	if len(dels) == 0 || (!blocked && (!limited || files <= allowed)) {
		return acts, st.setHeld(keep)
	}

	now := cs.c.opts.Now().UTC()
	rows := keep
	drop := map[int]bool{}
	var fresh []string
	for _, i := range dels {
		a := acts[i]
		h, ok := prev[a.Path]
		if !ok {
			h = HeldDelete{Path: a.Path, Kind: File, BaseHash: a.BaseHash, Since: now}
			if a.Op == OpRmdir {
				h.Kind = Dir
			}
			fresh = append(fresh, a.Path)
		}
		rows = append(rows, h)
		drop[i] = true
	}
	if err := st.setHeld(rows); err != nil {
		return nil, err
	}
	if len(fresh) > 0 {
		why := fmt.Sprintf("%d of %d files", files, n)
		if blocked {
			why = "the folder is already blocked"
		} else if limited {
			why += fmt.Sprintf(", more than the %d allowed", allowed)
		}
		cs.log("blocked", "", fmt.Sprintf("%d deletions held back (%s); shf status lists them, shf confirm sends them, shf restore-local brings the files back", len(fresh), why))
		for _, p := range fresh {
			cs.log("held", p, "deletion not sent")
		}
	}
	out := make([]Action, 0, len(acts)-len(drop))
	for i, a := range acts {
		if !drop[i] {
			out = append(out, a)
		}
	}
	return out, nil
}

func (c *Client) massDelete() MassDelete {
	if c.opts.MassDelete != nil {
		return *c.opts.MassDelete
	}
	return DefaultMassDelete
}

// HeldDeletes lists the deletions the guard holds back, by path.
func (s *State) HeldDeletes() ([]HeldDelete, error) {
	rows, err := s.db.Query(`SELECT path, kind, base_hash, confirmed, since FROM held_deletes ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeldDelete
	for rows.Next() {
		var h HeldDelete
		var k, since string
		if err := rows.Scan(&h.Path, &k, &h.BaseHash, &h.Confirmed, &since); err != nil {
			return nil, err
		}
		h.Kind = parseKind(k)
		h.Since, _ = time.Parse(time.RFC3339Nano, since)
		out = append(out, h)
	}
	return out, rows.Err()
}

// Blocked reports whether deletions wait for shf confirm or shf
// restore-local.
func (s *State) Blocked() (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM held_deletes WHERE confirmed = 0`).Scan(&n)
	return n > 0, err
}

// setHeld replaces the held deletions.
func (s *State) setHeld(rows []HeldDelete) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM held_deletes`); err != nil {
		return err
	}
	for _, h := range rows {
		if _, err := tx.Exec(`INSERT INTO held_deletes (path, kind, base_hash, confirmed, since) VALUES (?, ?, ?, ?, ?)`,
			h.Path, h.Kind.String(), h.BaseHash, h.Confirmed, h.Since.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ErrNothingHeld: shf confirm or restore-local found no held deletions.
var ErrNothingHeld = errors.New("no deletions are held back; the folder is not blocked")

// Confirm releases the held deletions and runs a cycle that sends them.
// It waits for a running cycle (the daemon's) to finish first. It returns
// how many deletions were released.
func (c *Client) Confirm(ctx context.Context) (int, *Result, error) {
	return c.resolveHeld(ctx, `UPDATE held_deletes SET confirmed = 1 WHERE confirmed = 0`)
}

// RestoreLocal brings the files of the held deletions back from the
// server and ends the block: their manifest rows are dropped, so the
// cycle that follows sees files the server has and this folder does not,
// and fetches them.
func (c *Client) RestoreLocal(ctx context.Context) (int, *Result, error) {
	return c.resolveHeld(ctx,
		`DELETE FROM manifest WHERE path IN (SELECT path FROM held_deletes)`,
		`DELETE FROM held_deletes`)
}

func (c *Client) resolveHeld(ctx context.Context, stmts ...string) (int, *Result, error) {
	unlock, err := lockWait(ctx, c.cfg.Dir)
	if err != nil {
		return 0, nil, err
	}
	defer unlock()
	tx, err := c.st.db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM held_deletes`).Scan(&n); err != nil {
		return 0, nil, err
	}
	if n == 0 {
		return 0, nil, ErrNothingHeld
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return 0, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	res, err := c.syncLocked(ctx, SyncOptions{})
	return n, res, err
}

// lockWait takes the folder's lock, waiting while another cycle holds it.
func lockWait(ctx context.Context, dir string) (func(), error) {
	for {
		unlock, err := lock(dir)
		if !errors.Is(err, ErrBusy) {
			return unlock, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ---- files still being written (8.3) ---------------------------------------

// sighting is the size and mtime a file had when first seen so.
type sighting struct {
	size, mtime int64
	at          time.Time
}

// settled reports whether a local file may be sent: its size and mtime
// have not changed for Options.Settle — its mtime is that old, or it was
// seen with the same size and mtime that long ago. Otherwise it returns
// when to look again.
func (c *Client) settled(p string, e Entry, now time.Time) (time.Time, bool) {
	d := c.opts.Settle
	if d <= 0 {
		return time.Time{}, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]sighting{}
	}
	s, ok := c.seen[p]
	if !ok || s.size != e.Size || s.mtime != e.Mtime {
		s = sighting{size: e.Size, mtime: e.Mtime, at: now}
		c.seen[p] = s
	}
	mt := time.Unix(0, e.Mtime)
	if now.Sub(mt) >= d || now.Sub(s.at) >= d {
		delete(c.seen, p)
		return time.Time{}, true
	}
	until := s.at.Add(d)
	if u := mt.Add(d); u.Before(until) {
		until = u
	}
	return until, false
}

// holdUnsettled takes out of a plan the uploads of files that are still
// being written; the result says when to try again.
func (cs *cycleState) holdUnsettled(acts []Action) []Action {
	if cs.c.opts.Settle <= 0 {
		return acts
	}
	now := cs.c.opts.Now()
	out := acts[:0]
	for _, a := range acts {
		switch a.Op {
		case OpUpload, OpConflict, OpMove:
			if until, ok := cs.c.settled(a.Path, a.Local, now); !ok {
				cs.res.Unsettled++
				if cs.res.RetryAt.IsZero() || until.Before(cs.res.RetryAt) {
					cs.res.RetryAt = until
				}
				continue
			}
		}
		out = append(out, a)
	}
	return out
}
