package shfcli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/digital-cube/shared-folder/internal/client"
)

// Accounts (docs/specifikacija.md, section 6):
//
//	shf login <url> [--user U] [--device-name N] [--password-stdin] [--migrate|--no-migrate]
//	shf logout <url> [--local]
//	shf folders [<url>]

// lines reads answers from env.Stdin, one line at a time; without one,
// every answer is empty (io.EOF).
type lines struct{ r *bufio.Reader }

func newLines(env Env) *lines {
	if env.Stdin == nil {
		return &lines{}
	}
	return &lines{r: bufio.NewReader(env.Stdin)}
}

func (l *lines) next() (string, error) {
	if l.r == nil {
		return "", io.EOF
	}
	s, err := l.r.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// readPassword reads a password from the terminal without echo.
func (env Env) readPassword(prompt string) (string, error) {
	if env.ReadPassword != nil {
		return env.ReadPassword(prompt)
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("no terminal to read the password from; pipe it in with --password-stdin")
	}
	fmt.Fprint(env.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(env.Stderr)
	return string(b), err
}

func login(ctx context.Context, args []string, env Env) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	user := fs.String("user", "", "the account's username (asked when missing)")
	devName := fs.String("device-name", "", "this device's name on the server (default: the hostname)")
	pwStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin (a temporary one's replacement from the second)")
	migrate := fs.Bool("migrate", false, "move every folder joined with a member's token on this server to the account, without asking")
	noMigrate := fs.Bool("no-migrate", false, "leave folders joined with a member's token alone, without asking")
	pos, err := parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	if *migrate && *noMigrate {
		return usageError("login: --migrate and --no-migrate exclude each other")
	}
	if *pwStdin && *user == "" {
		return usageError("login: --password-stdin needs --user")
	}
	base, _, err := client.ServerKey(pos[0])
	if err != nil {
		return err
	}
	home, err := client.StateHome(env.Getenv)
	if err != nil {
		return err
	}
	in := newLines(env)
	w := env.Stdout
	if *user == "" {
		fmt.Fprint(w, "username: ")
		if *user, err = in.next(); err != nil || strings.TrimSpace(*user) == "" {
			return errors.New("no username given (--user)")
		}
		*user = strings.TrimSpace(*user)
	}
	if *devName == "" {
		*devName, _ = os.Hostname()
	}
	password := func(prompt string) (string, error) {
		if *pwStdin {
			pw, err := in.next()
			if err != nil || pw == "" {
				return "", fmt.Errorf("no password on stdin for %q", strings.TrimSuffix(prompt, ": "))
			}
			return pw, nil
		}
		return env.readPassword(prompt)
	}
	pw, err := password("password for " + *user + "@" + base + ": ")
	if err != nil {
		return err
	}
	hc := env.Options.HTTPClient
	acct, mustChange, err := client.SignIn(ctx, client.SignInRequest{URL: base, Username: *user, Password: pw, DeviceName: *devName, HTTPClient: hc})
	if err != nil {
		return err
	}
	if mustChange {
		if err := changeTemporary(ctx, env, acct, pw, password, *pwStdin); err != nil {
			acct.SignOut(ctx, hc) // the token is good for nothing else; do not leave it behind
			return err
		}
	}
	if old, err := client.LoadAccount(home, base); err == nil && old.Token != acct.Token {
		// one sign-in per server: the earlier device is revoked, its
		// folders take the new one's credentials below
		if err := old.SignOut(ctx, hc); err != nil {
			fmt.Fprintf(env.Stderr, "shf: warning: the earlier sign-in (device %s, id %d) was not revoked on the server: %v\n", old.DeviceName, old.DeviceID, err)
		}
	}
	if err := client.SaveAccount(home, acct); err != nil {
		return err
	}
	fmt.Fprintf(w, "signed in to %s as %s (device %s, id %d)\n", base, acct.Username, acct.DeviceName, acct.DeviceID)

	me, err := acct.Me(ctx, hc)
	if err != nil {
		return err
	}
	plan, err := client.PlanLinks(ctx, home, acct, me, hc)
	if err != nil {
		return err
	}
	failed := 0
	relink := func(c client.LinkCandidate, done string) {
		if err := client.Relink(ctx, c.Config, acct, env.Options); err != nil {
			failed++
			fmt.Fprintf(env.Stderr, "shf: %s: %v\n", c.Config.Folder, err)
			return
		}
		fmt.Fprintf(w, "%s: %s (folder %s, member %s, device %s)\n", c.Config.Folder, done, c.Folder.Name, c.Config.Member, acct.DeviceName)
	}
	for _, c := range plan.Again {
		relink(c, "signed in again")
	}
	var skipped []string
	for _, c := range plan.Legacy {
		was := "a member's own token"
		if c.LegacyMember != "" {
			was = "member " + c.LegacyMember + "'s own token"
		}
		yes := *migrate
		if !*migrate && !*noMigrate {
			fmt.Fprintf(w, "%s (folder %s) was joined with %s; your account is member %s there.\n"+
				"Move it to your account (this device's own token and SSH key; the files stay as they are)? [Y/n] ",
				c.Config.Folder, c.Folder.Name, was, c.Folder.Member)
			a, err := in.next()
			a = strings.ToLower(strings.TrimSpace(a))
			yes = err == nil && (a == "" || a == "y" || a == "yes" || a == "d" || a == "da")
			if err != nil {
				fmt.Fprintln(w)
			}
		}
		if !yes {
			skipped = append(skipped, c.Config.Folder)
			continue
		}
		relink(c, "moved to the account")
	}
	if len(skipped) > 0 {
		fmt.Fprintf(w, "left on their member's token: %s\n  (shf login %s --migrate moves them later)\n", strings.Join(skipped, ", "), base)
	}
	for _, n := range plan.Notes {
		fmt.Fprintf(w, "%s\n", n)
	}
	if failed > 0 {
		return fmt.Errorf("signed in, but %d folders could not be given this device's credentials (see above)", failed)
	}
	return nil
}

// changeTemporary replaces a temporary password: asked twice on a
// terminal, read from the next line of stdin with --password-stdin.
func changeTemporary(ctx context.Context, env Env, acct *client.Account, old string, password func(string) (string, error), fromStdin bool) error {
	fmt.Fprintln(env.Stdout, "the account has a temporary password; choose your own now (12 to 256 characters, not the username)")
	for try := 1; ; try++ {
		pw, err := password("new password: ")
		if err != nil {
			if fromStdin {
				return fmt.Errorf("%w (the temporary password must be replaced: give the new one on the second line)", err)
			}
			return err
		}
		if !fromStdin {
			again, err := password("new password again: ")
			if err != nil {
				return err
			}
			if again != pw {
				fmt.Fprintln(env.Stderr, "shf: the two passwords differ")
				if try < 3 {
					continue
				}
				return errors.New("the password was not changed")
			}
		}
		err = acct.ChangePassword(ctx, env.Options.HTTPClient, old, pw)
		var ae *client.APIError
		if errors.As(err, &ae) && ae.Code == "invalid_password" {
			if fromStdin || try >= 3 {
				return fmt.Errorf("the new password was refused: %s", ae.Message)
			}
			fmt.Fprintf(env.Stderr, "shf: the new password was refused: %s\n", ae.Message)
			continue
		}
		if err != nil {
			return fmt.Errorf("changing the password: %w", err)
		}
		fmt.Fprintln(env.Stdout, "password changed")
		return nil
	}
}

func logout(ctx context.Context, args []string, env Env) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	local := fs.Bool("local", false, "forget the sign-in here even when the server cannot be reached (then revoke the device elsewhere)")
	pos, err := parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	home, err := client.StateHome(env.Getenv)
	if err != nil {
		return err
	}
	acct, err := client.LoadAccount(home, pos[0])
	if err != nil {
		return err
	}
	if err := acct.SignOut(ctx, env.Options.HTTPClient); err != nil {
		if !*local {
			return fmt.Errorf("the server did not revoke this device: %w\nnothing changed; shf logout --local forgets the sign-in anyway (then revoke the device %q from another sign-in)", err, acct.DeviceName)
		}
		fmt.Fprintf(env.Stderr, "shf: warning: the device %s (id %d) is still signed in on the server: %v\n", acct.DeviceName, acct.DeviceID, err)
	}
	if err := client.RemoveAccount(home, acct.URL); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "signed out of %s (device %s, id %d)\n", acct.URL, acct.DeviceName, acct.DeviceID)
	joined, err := client.Joined(home)
	if err != nil {
		return err
	}
	var stay []string
	for _, c := range joined {
		if c.Account != "" && c.SignedOutLocally() {
			stay = append(stay, c.Folder)
		}
	}
	if len(stay) > 0 {
		fmt.Fprintf(env.Stdout, "these folders stay on disk but are not synchronized until shf login %s:\n", acct.URL)
		for _, f := range stay {
			fmt.Fprintf(env.Stdout, "  %s\n", f)
		}
	}
	return nil
}

func folders(ctx context.Context, args []string, env Env) error {
	fs := flag.NewFlagSet("folders", flag.ContinueOnError)
	pos, err := parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	home, err := client.StateHome(env.Getenv)
	if err != nil {
		return err
	}
	var accts []*client.Account
	if len(pos) == 1 {
		a, err := client.LoadAccount(home, pos[0])
		if errors.Is(err, client.ErrNoAccount) {
			return fmt.Errorf("%w: shf login %s first", err, pos[0])
		}
		if err != nil {
			return err
		}
		accts = append(accts, a)
	} else if accts, err = client.Accounts(home); err != nil {
		return err
	}
	if len(accts) == 0 {
		return errors.New("not signed in to any server: shf login <url> first")
	}
	joined, err := client.Joined(home)
	if err != nil {
		return err
	}
	w := env.Stdout
	var errs []error
	for i, a := range accts {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s — %s (device %s)\n", a.URL, a.Username, a.DeviceName)
		me, err := a.Me(ctx, env.Options.HTTPClient)
		if err != nil {
			fmt.Fprintf(w, "  %v\n", err)
			errs = append(errs, err)
			continue
		}
		if len(me.Folders) == 0 {
			fmt.Fprintln(w, "  no folders yet: a folder's admin adds the account")
			continue
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  NAME\tROLE\tSHARE-ID\tJOINED")
		for _, f := range me.Folders {
			where := "no"
			for _, c := range joined {
				if c.ShareID != f.ShareID {
					continue
				}
				where = c.Folder
				switch {
				case c.Account == "":
					where += " (member token; shf login --migrate moves it)"
				case c.SignedOutLocally() || c.DeviceID != a.DeviceID:
					where += " (signed out)"
				}
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", f.Name, f.Role, f.ShareID, where)
		}
		tw.Flush()
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
