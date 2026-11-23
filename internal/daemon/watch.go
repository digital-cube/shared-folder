package daemon

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/text/unicode/norm"

	"github.com/digital-cube/shared-folder/internal/ignore"
)

// watch reports changes in the folder to the loop. It watches every
// directory that is not ignored (inotify is not recursive), adds new ones
// as they appear and re-reads .shfignore when one changes. When the root
// is missing it reports one change, so the next cycle says the folder is
// unavailable, and tries again every Settings.Poll.
func (f *folder) watch(ctx context.Context) {
	for ctx.Err() == nil {
		err := f.watchOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		f.log.Warn("not watching the folder; changes are picked up by the next cycle", "err", err)
		f.changed("") // let a cycle look (and report a missing folder)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(f.set.Poll)):
		}
	}
}

func (f *folder) watchOnce(ctx context.Context) error {
	root := f.fc.Folder
	if fi, err := os.Stat(root); err != nil {
		return err
	} else if !fi.IsDir() {
		return errors.New(root + " is not a directory")
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	m, err := ignore.FromDir(root)
	if err != nil {
		return err
	}
	if err := f.addTree(w, m, root, ""); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-w.Errors:
			if !ok {
				return errors.New("watcher closed")
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				f.changed("") // events were lost: a cycle scans everything
				continue
			}
			f.log.Warn("watcher", "err", err)
		case ev, ok := <-w.Events:
			if !ok {
				return errors.New("watcher closed")
			}
			if ev.Op == fsnotify.Chmod {
				continue // times or permissions; not content
			}
			rel, err := filepath.Rel(root, ev.Name)
			if err != nil {
				continue
			}
			if rel == "." {
				if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
					return errors.New("the folder itself was removed or moved")
				}
				continue
			}
			p := norm.NFC.String(filepath.ToSlash(rel))
			fi, statErr := os.Lstat(ev.Name)
			isDir := statErr == nil && fi.IsDir()
			if m.Match(p, isDir) {
				continue
			}
			if path.Base(p) == ignore.FileName {
				if m2, err := ignore.FromDir(root); err == nil {
					m = m2
				}
			}
			if isDir && ev.Has(fsnotify.Create) {
				// a new (or moved in) directory: watch it and below
				if err := f.addTree(w, m, ev.Name, p); err != nil {
					f.log.Warn("watching new directory", "path", p, "err", err)
				}
			}
			f.changed(p)
		}
	}
}

// addTree watches dir (whose slash path below the root is rel) and every
// directory below it that is neither ignored nor a symbolic link.
func (f *folder) addTree(w *fsnotify.Watcher, m *ignore.Matcher, dir, rel string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			return nil // gone meanwhile, or unreadable: the cycle reports it
		}
		if !d.IsDir() {
			return nil
		}
		r, _ := filepath.Rel(dir, p)
		sub := rel
		if r != "." {
			sub = path.Join(rel, filepath.ToSlash(r))
		}
		if sub != "" && m.Match(norm.NFC.String(sub), true) {
			return filepath.SkipDir
		}
		if err := w.Add(p); err != nil {
			if p == dir {
				return err
			}
			f.log.Warn("cannot watch directory", "path", sub, "err", err)
		}
		return nil
	})
}
