package shfdcli

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/mail"
	"github.com/digital-cube/shared-folder/internal/server"
)

// Mail of a forgotten password (docs/admin.md, "Zaboravljena lozinka").
const (
	EnvBrevoKeyFile = "SHFD_BREVO_KEY_FILE" // --brevo-key-file: the file with Brevo's API key
	EnvMailFrom     = "SHFD_MAIL_FROM"      // --mail-from
	EnvPublicURL    = "SHFD_PUBLIC_URL"     // --public-url: https://host the links point to
	EnvGUIBase      = "SHFD_GUI_BASE"       // --gui-base: the base path of shfd-gui (default /gui)
	// EnvBrevoURL replaces Brevo's endpoint; only for tests (a fake
	// server). It has no flag.
	EnvBrevoURL = "SHFD_BREVO_URL"
)

// DefaultGUIBase is where shfd-gui is unless SHFD_GUI_BASE says otherwise
// (as in shfd-gui itself).
const DefaultGUIBase = "/gui"

// mailFlags are the flags of mail: the key is only ever read from a file,
// never from a flag's or a variable's value.
type mailFlags struct {
	keyFile, from, publicURL, guiBase string
}

func (m *mailFlags) register(fs *flag.FlagSet, env Env) {
	fs.StringVar(&m.keyFile, "brevo-key-file", env.Getenv(EnvBrevoKeyFile), "file with Brevo's API key (root:shfd 0640); without it no mail is sent")
	fs.StringVar(&m.from, "mail-from", cmp.Or(env.Getenv(EnvMailFrom), mail.DefaultFrom), "sender of the mails")
	m.registerLink(fs, env)
}

func (m *mailFlags) registerLink(fs *flag.FlagSet, env Env) {
	fs.StringVar(&m.publicURL, "public-url", env.Getenv(EnvPublicURL), "public https:// URL of the server; the links in mails point there")
	gb := env.Getenv(EnvGUIBase)
	if gb == "" {
		gb = DefaultGUIBase
	}
	fs.StringVar(&m.guiBase, "gui-base", gb, `base path of shfd-gui under the public URL ("/" at the root)`)
}

// sender is the Brevo sender of the flags, nil without --brevo-key-file.
// warn is set when the key file can be read by any user.
func (m *mailFlags) sender(env Env) (s *mail.Brevo, warn string, err error) {
	if m.keyFile == "" {
		return nil, "", nil
	}
	key, world, err := mail.LoadKey(m.keyFile)
	if err != nil {
		return nil, "", err
	}
	from, err := mail.ParseAddress(m.from)
	if err != nil {
		return nil, "", usageError("--mail-from: %v", err)
	}
	if world {
		warn = m.keyFile + " can be read by every user of the machine; make it root:shfd 0640"
	}
	return mail.NewBrevo(key, from, env.Getenv(EnvBrevoURL)), warn, nil
}

// resetURL is the start of a reset link ("" without --public-url).
func (m *mailFlags) resetURL() (string, error) {
	if m.publicURL == "" {
		return "", nil
	}
	return server.ResetPageURL(m.publicURL, m.guiBase)
}

// mailCmd: shfd mail test <address> sends a test mail with the settings of
// serve, to check the key and the sender.
func mailCmd(args []string, env Env) error {
	if len(args) == 0 || args[0] != "test" {
		return usageError("mail: want test <address>")
	}
	fs := flag.NewFlagSet("mail test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var mf mailFlags
	mf.register(fs, env)
	var pos []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			return usageError("mail test: %v", err)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(pos) != 1 {
		return usageError("mail test: want one address")
	}
	to, err := mail.ParseAddress(pos[0])
	if err != nil {
		return usageError("mail test: %v", err)
	}
	if mf.keyFile == "" {
		return usageError("mail test: --brevo-key-file or $%s is required", EnvBrevoKeyFile)
	}
	snd, warn, err := mf.sender(env)
	if err != nil {
		return err
	}
	if warn != "" {
		fmt.Fprintf(env.Stderr, "warning: %s\n", warn)
	}
	site := ""
	if u, err := url.Parse(mf.publicURL); err == nil && u.Host != "" {
		site = u.Host
	} else if h, err := os.Hostname(); err == nil {
		site = h
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := snd.Send(ctx, mail.TestMail(to, site)); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "sent a test mail to %s from %s\n", to.Email, snd.From())
	return nil
}

// printResetLink prints a link of shfd user reset-link.
func printResetLink(env Env, username, resetURL, guiBase, token string, expires time.Time) {
	if resetURL == "" {
		path := "/"
		if b := strings.Trim(guiBase, "/"); b != "" {
			path = "/" + b + "/"
		}
		fmt.Fprintf(env.Stdout, "account: %s\nlink:    <public URL>%s%s%s\n\nSet --public-url (or $%s) to get the whole link.\n",
			username, path, server.ResetFragment, token, EnvPublicURL)
	} else {
		fmt.Fprintf(env.Stdout, "account: %s\nlink:    %s%s%s\n", username, resetURL, server.ResetFragment, token)
	}
	fmt.Fprintf(env.Stdout, "valid until %s (%d minutes), once. Earlier links of the account no longer work.\nNo mail was sent: hand the link to the user over a private channel.\n",
		expires.UTC().Format(time.RFC3339), int(server.ResetValidity/time.Minute))
}
