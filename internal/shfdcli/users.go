package shfdcli

import (
	"flag"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/digital-cube/shared-folder/internal/server"
)

// userCmd manages accounts (docs/admin.md, "Nalozi"):
//
//	shfd user add <username> [--display-name NAME]
//	shfd user passwd <username>
//	shfd user disable|enable <username> [--authorized-keys FILE]
//	shfd user list
//	shfd user show <username>
//
// Like the member commands they work on the databases directly, next to a
// running shfd serve: it reads accounts and devices on every request, so
// a disable takes effect at once.
func userCmd(args []string, env Env) error {
	if len(args) == 0 {
		return usageError("user: want add, passwd, disable, enable, list or show")
	}
	open := func(root roots) (*server.Server, error) {
		return root.open(server.Options{SkipRecovery: true})
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("user add", flag.ContinueOnError)
		display := fs.String("display-name", "", "the name shown next to the username")
		root, pos, err := flags(fs, args[1:], 1, env)
		if err != nil {
			return err
		}
		srv, err := open(root)
		if err != nil {
			return err
		}
		defer srv.Close()
		u, temp, err := srv.AddUser(pos[0], *display)
		if err != nil {
			return err
		}
		printTempPassword(env, u.Username, temp)
		return nil
	case "passwd":
		fs := flag.NewFlagSet("user passwd", flag.ContinueOnError)
		root, pos, err := flags(fs, args[1:], 1, env)
		if err != nil {
			return err
		}
		srv, err := open(root)
		if err != nil {
			return err
		}
		defer srv.Close()
		temp, err := srv.ResetPassword(pos[0])
		if err != nil {
			return err
		}
		printTempPassword(env, pos[0], temp)
		fmt.Fprintf(env.Stdout, "\nThe old password no longer works. Signed-in devices stay signed in, but\nget nothing but \"password change required\" until the password is changed.\n")
		return nil
	case "disable", "enable":
		fs := flag.NewFlagSet("user "+args[0], flag.ContinueOnError)
		var sf sshFlags
		sf.register(fs, env)
		root, pos, err := flags(fs, args[1:], 1, env)
		if err != nil {
			return err
		}
		// disable removes the lines of the account's devices from
		// authorized_keys.
		opts, err := sf.options(root, env)
		if err != nil {
			return err
		}
		opts.SkipRecovery = true
		srv, err := root.open(opts)
		if err != nil {
			return err
		}
		defer srv.Close()
		if args[0] == "enable" {
			if err := srv.EnableUser(pos[0]); err != nil {
				return err
			}
			fmt.Fprintf(env.Stdout, "enabled %s; its devices stay signed out\n", pos[0])
			return nil
		}
		n, err := srv.DisableUser(pos[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "disabled %s; signed out %d %s, and their folder credentials and SSH keys are revoked\n", pos[0], n, plural(n, "device", "devices"))
		return nil
	case "list":
		fs := flag.NewFlagSet("user list", flag.ContinueOnError)
		root, _, err := flags(fs, args[1:], 0, env)
		if err != nil {
			return err
		}
		srv, err := open(root)
		if err != nil {
			return err
		}
		defer srv.Close()
		us, err := srv.Users()
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "USERNAME\tDISPLAY NAME\tSTATUS\tLAST LOGIN\tDEVICES")
		for _, u := range us {
			devs, err := srv.Devices(u.ID, false)
			if err != nil {
				return err
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", u.Username, orDash(u.DisplayName), userStatus(&u), when(u.LastLoginAt), len(devs))
		}
		return tw.Flush()
	case "show":
		fs := flag.NewFlagSet("user show", flag.ContinueOnError)
		root, pos, err := flags(fs, args[1:], 1, env)
		if err != nil {
			return err
		}
		srv, err := open(root)
		if err != nil {
			return err
		}
		defer srv.Close()
		u, err := srv.UserByName(pos[0])
		if err != nil {
			return err
		}
		folders, err := srv.UserFolders(u.ID)
		if err != nil {
			return err
		}
		devs, err := srv.Devices(u.ID, true)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "username:     %s\ndisplay name: %s\nstatus:       %s\ncreated:      %s\nlast login:   %s\n",
			u.Username, orDash(u.DisplayName), userStatus(u), u.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), when(u.LastLoginAt))
		fmt.Fprintf(env.Stdout, "\nFolders:\n")
		tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SHARE ID\tNAME\tMEMBER\tROLE")
		for _, f := range folders {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.ShareID, f.Name, f.Member, f.Role)
		}
		tw.Flush()
		fmt.Fprintf(env.Stdout, "\nDevices:\n")
		tw = tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tKIND\tCREATED\tLAST SEEN\tSTATUS")
		now := time.Now()
		for _, d := range devs {
			st := "active"
			switch {
			case d.RevokedAt != nil:
				st = "signed out " + when(d.RevokedAt)
			case !d.Active(now):
				st = "expired " + when(d.ExpiresAt)
			case d.ExpiresAt != nil:
				st = "active until " + when(d.ExpiresAt)
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Kind, when(&d.CreatedAt), when(d.LastSeenAt), st)
		}
		return tw.Flush()
	}
	return usageError("user: unknown subcommand %q", args[0])
}

func userStatus(u *server.User) string {
	switch {
	case u.DisabledAt != nil:
		return "disabled " + when(u.DisabledAt)
	case u.MustChange:
		return "active (temporary password)"
	}
	return "active"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printTempPassword(env Env, username, temp string) {
	fmt.Fprintf(env.Stdout, "account:            %s\ntemporary password: %s\n\nThe password is shown only now; it is stored as a hash. It must be\nchanged at the first sign-in.\n", username, temp)
}
