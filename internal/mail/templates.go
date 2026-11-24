package mail

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// The mails of accounts, in Serbian (the default) and English. Each has an
// HTML and a plain-text body with the same content.

// Langs a mail is written in.
const (
	LangSR = "sr"
	LangEN = "en"
)

// NormLang is "en" for "en" (or "en-…"), "sr" for anything else.
func NormLang(lang string) string {
	if l := strings.ToLower(strings.TrimSpace(lang)); l == LangEN || strings.HasPrefix(l, LangEN+"-") {
		return LangEN
	}
	return LangSR
}

// Recipient is the account a mail is about.
type Recipient struct {
	Username    string
	DisplayName string
	Email       string
}

func (r Recipient) address() Address { return Address{Name: r.DisplayName, Email: r.Email} }

// who is "Ime Prezime (marko)", or "marko" without a display name.
func (r Recipient) who() string {
	if r.DisplayName == "" {
		return r.Username
	}
	return r.DisplayName + " (" + r.Username + ")"
}

// minutes is a whole number of minutes, at least 1.
func minutes(d time.Duration) int { return max(int(d.Round(time.Minute)/time.Minute), 1) }

// ResetMail is the link of a forgotten password: for whom, the link, how
// long it is valid and "ignore it if you did not ask". site names the
// server (the host of the public URL).
func ResetMail(lang string, r Recipient, link string, valid time.Duration, site string) Message {
	n := minutes(valid)
	var c content
	if NormLang(lang) == LangEN {
		c = content{
			Subject: "Reset your password on " + site,
			Lines: []string{
				"Hello " + r.who() + ",",
				"someone asked to reset the password of the account " + r.Username + " on " + site + ". Open the link below to choose a new one:",
			},
			Link:      link,
			LinkLabel: "Choose a new password",
			After: []string{
				fmt.Sprintf("The link is valid for %d minutes and can be used once.", n),
				"If you did not ask for this, ignore this mail: your password stays as it is.",
			},
		}
	} else {
		c = content{
			Subject: "Nova lozinka za " + site,
			Lines: []string{
				"Zdravo " + r.who() + ",",
				"zatražena je nova lozinka za nalog " + r.Username + " na " + site + ". Otvorite link ispod i izaberite novu:",
			},
			Link:      link,
			LinkLabel: "Izaberi novu lozinku",
			After: []string{
				fmt.Sprintf("Link važi %d minuta i može da se iskoristi jednom.", n),
				"Ako niste vi tražili novu lozinku, ignorišite ovaj mejl: lozinka ostaje ista.",
			},
		}
	}
	return c.message(r.address())
}

// PasswordChangedMail tells the account that its password was changed
// through a reset link, at (UTC) from ip, and that its browser sessions
// were signed out.
func PasswordChangedMail(lang string, r Recipient, at time.Time, ip, site string) Message {
	when := at.UTC().Format("2006-01-02 15:04") + " UTC"
	var c content
	if NormLang(lang) == LangEN {
		from := ""
		if ip != "" {
			from = " from the address " + ip
		}
		c = content{
			Subject: "Your password on " + site + " was changed",
			Lines: []string{
				"Hello " + r.who() + ",",
				"the password of the account " + r.Username + " on " + site + " was changed at " + when + from + ", with a link from a forgotten-password mail.",
				"Every browser session of the account was signed out. Client devices (shf) stay signed in.",
			},
			After: []string{"If this was not you, tell the server's admin at once."},
		}
	} else {
		from := ""
		if ip != "" {
			from = " sa adrese " + ip
		}
		c = content{
			Subject: "Lozinka za " + site + " je promenjena",
			Lines: []string{
				"Zdravo " + r.who() + ",",
				"lozinka naloga " + r.Username + " na " + site + " je promenjena " + when + from + ", linkom iz mejla za zaboravljenu lozinku.",
				"Sve sesije naloga u browseru su odjavljene. Klijentski uređaji (shf) ostaju prijavljeni.",
			},
			After: []string{"Ako to niste bili vi, odmah se javite adminu servera."},
		}
	}
	return c.message(r.address())
}

// TestMail is what shfd mail test sends.
func TestMail(to Address, site string) Message {
	c := content{
		Subject: "shfd: probni mejl / test mail",
		Lines: []string{
			"Ovo je probni mejl servera " + site + " (shfd mail test). Slanje mejlova radi.",
			"This is a test mail of the server " + site + " (shfd mail test). Sending mail works.",
		},
	}
	return c.message(to)
}

// content is a mail before it is written out: paragraphs, an optional
// link (a button in HTML), more paragraphs.
type content struct {
	Subject   string
	Lines     []string
	Link      string
	LinkLabel string
	After     []string
}

func (c content) message(to Address) Message {
	var t strings.Builder
	for _, l := range c.Lines {
		t.WriteString(l + "\n\n")
	}
	if c.Link != "" {
		t.WriteString(c.Link + "\n\n")
	}
	for _, l := range c.After {
		t.WriteString(l + "\n\n")
	}
	t.WriteString("-- \nshfd\n")
	var h bytes.Buffer
	if err := htmlMail.Execute(&h, c); err != nil {
		panic(err) // the template and its data are ours
	}
	return Message{To: to, Subject: c.Subject, Text: t.String(), HTML: h.String()}
}

var htmlMail = template.Must(template.New("mail").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Subject}}</title></head>
<body style="margin:0;padding:24px;background:#f4f5f7;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1d2330;font-size:15px;line-height:1.5">
<div style="max-width:560px;margin:0 auto;background:#ffffff;border-radius:8px;padding:28px">
{{range .Lines}}<p style="margin:0 0 14px">{{.}}</p>
{{end}}{{if .Link}}<p style="margin:22px 0"><a href="{{.Link}}" style="display:inline-block;background:#2f6fde;color:#ffffff;text-decoration:none;padding:10px 18px;border-radius:6px;font-weight:600">{{.LinkLabel}}</a></p>
<p style="margin:0 0 14px;font-size:13px;color:#5b6475;word-break:break-all">{{.Link}}</p>
{{end}}{{range .After}}<p style="margin:0 0 14px">{{.}}</p>
{{end}}<p style="margin:22px 0 0;font-size:12px;color:#8a92a3">shfd</p>
</div>
</body></html>
`))
