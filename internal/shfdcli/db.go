package shfdcli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/digital-cube/shared-folder/internal/server"
)

func dbCmd(args []string, env Env) error {
	if len(args) == 0 || args[0] != "move" {
		return usageError("db: want move")
	}
	fs := flag.NewFlagSet("db move", flag.ContinueOnError)
	to := fs.String("to", "", "new db-root, on a local disk")
	root, _, err := flags(fs, args[1:], 0, env)
	if err != nil {
		return err
	}
	if *to == "" {
		return usageError("db move: --to DIR is required")
	}
	abs, from, err := root.abs()
	if err != nil {
		return err
	}
	toAbs, err := filepath.Abs(*to)
	if err != nil {
		return err
	}
	// serve holds serve.lock in the db-root it runs on; taking it here
	// proves none runs, and keeps one from starting during the copy.
	if _, err := os.Stat(from); err != nil {
		return fmt.Errorf("db-root %s: %w", from, err)
	}
	unlock, err := lockDataRoot(from)
	if err != nil {
		return fmt.Errorf("%w; stop shfd serve before moving its databases", err)
	}
	defer unlock()
	if err := os.MkdirAll(toAbs, 0o700); err != nil {
		return err
	}
	unlockTo, err := lockDataRoot(toAbs)
	if err != nil {
		return fmt.Errorf("%w; stop the shfd serve that uses it", err)
	}
	defer unlockTo()
	moved, err := server.MoveDBs(abs, from, toAbs)
	if err != nil {
		return err
	}
	w := env.Stdout
	for _, m := range moved {
		fmt.Fprintf(w, "copied %s\n  %s\n  -> %s (integrity_check ok)\n", m.Name, m.From, m.To)
	}
	fmt.Fprintf(w, `
%d database(s) copied to %s. The originals in %s were left
in place; delete them (*.db, *.db-wal, *.db-shm) once shfd runs well on the copies.

Before starting shfd again, give every shfd command the new db-root. In the
systemd unit of shfd serve:

    Environment=SHFD_DB_ROOT=%s

(or --db-root %s on the ExecStart line), then systemctl daemon-reload.
`, len(moved), toAbs, from, toAbs, toAbs)
	if toAbs == abs {
		fmt.Fprintf(w, "\nThe databases are back in the data-root: remove SHFD_DB_ROOT / --db-root instead.\n")
	}
	fmt.Fprintf(w, `
With rsync over SSH, rewrite the forced commands in authorized_keys, which
carry the db-root, as the user shfd runs as:

    shfd keys sync --data-root %s --db-root %s --authorized-keys <file>

The data-root now names the new db-root in %s, so a server started
without it is refused rather than run on the old copies.
`, abs, toAbs, filepath.Join(abs, server.MovedMarker))
	return nil
}
