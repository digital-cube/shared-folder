package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Moving joined folders to an account (docs/specifikacija.md, 8.4): after
// shf login, a folder of that server joined with a member's own token
// (legacy) may be given credentials of this device instead, and a folder
// joined by account before shf logout gets the new sign-in's. Neither
// joins again nor fetches the files again: only the token and the SSH
// key of the folder change.

// LinkCandidate is a joined folder of the server whose account is a
// member of it.
type LinkCandidate struct {
	Config *Config
	Folder AccountFolder
	// LegacyMember is the member whose own token the folder was joined
	// with ("" when the server did not say, or for an account folder).
	LegacyMember string
}

// LinkPlan sorts the folders joined under home on the account's server.
type LinkPlan struct {
	// Again are folders joined by this account before (an earlier
	// sign-in of this device): shf login gives them the new device's
	// credentials without asking.
	Again []LinkCandidate
	// Legacy are folders joined with a member's own token in which the
	// account is a member: shf login offers to move them over.
	Legacy []LinkCandidate
	// Notes are folders of the server it leaves alone, and why.
	Notes []string
}

// PlanLinks looks at the folders joined under home on acct's server.
func PlanLinks(ctx context.Context, home string, acct *Account, me *Me, hc *http.Client) (*LinkPlan, error) {
	joined, err := Joined(home)
	if err != nil {
		return nil, err
	}
	byID := map[string]AccountFolder{}
	for _, f := range me.Folders {
		byID[f.ShareID] = f
	}
	p := &LinkPlan{}
	for _, c := range joined {
		if !sameServer(c.URL, acct.URL) {
			continue
		}
		f, member := byID[c.ShareID]
		switch {
		case c.Account != "" && c.Account != acct.Username:
			p.Notes = append(p.Notes, fmt.Sprintf("%s: joined by account %s, not %s; left alone", c.Folder, c.Account, acct.Username))
		case c.Account != "" && c.DeviceID == acct.DeviceID:
			// already this sign-in's
		case c.Account != "" && !member:
			p.Notes = append(p.Notes, fmt.Sprintf("%s: the account is no longer a member of this folder; it stays signed out", c.Folder))
		case c.Account != "":
			p.Again = append(p.Again, LinkCandidate{Config: c, Folder: f})
		case member:
			p.Legacy = append(p.Legacy, LinkCandidate{Config: c, Folder: f, LegacyMember: legacyMember(ctx, c, hc)})
		}
	}
	return p, nil
}

// legacyMember asks the server whose token the folder has (GET /v1/whoami).
func legacyMember(ctx context.Context, c *Config, hc *http.Client) string {
	var w struct {
		Username string `json:"username"`
	}
	base, _, err := ServerKey(c.URL)
	if err != nil || doJSON(ctx, httpClient(hc), http.MethodGet, base+"/v1/whoami", c.Token, nil, &w) != nil {
		return ""
	}
	return w.Username
}

// Relink gives a joined folder the credentials of the account's device: a
// folder token of its own (POST /v1/shares/{id}/devices) and a new SSH
// key registered with it, in place of the token and key it had, which are
// deleted here. The server keeps a member's own (legacy) token and key
// until an admin revokes them. The folder's files and state stay: there
// is no new join and nothing is fetched again. A running shf daemon
// takes the new credentials up on its own.
func Relink(ctx context.Context, cfg *Config, acct *Account, opts Options) error {
	if !sameServer(cfg.URL, acct.URL) {
		return fmt.Errorf("%s is a folder of %s, the account one of %s", cfg.Folder, cfg.URL, acct.URL)
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return err
	}
	fd, err := acct.issueFolderDevice(ctx, opts.HTTPClient, cfg.ShareID)
	if err != nil {
		return err
	}
	next := *cfg
	next.setDevice(acct, fd)
	a := newAPI(cfg.URL, cfg.ShareID, fd.Token, opts.HTTPClient)
	if _, err := a.head(ctx); err != nil {
		return fmt.Errorf("the server refused the new folder token: %w", err)
	}
	if opts.Transport == nil {
		var over SSH
		if cfg.SSH != nil {
			over = *cfg.SSH
		}
		// A running cycle may go on meanwhile: its token still works and
		// the new key takes the old one's place in one rename.
		if next.SSH, err = setupSSH(ctx, a, &next, u, over, nil, true); err != nil {
			return err
		}
	}
	if err := next.save(); err != nil {
		return err
	}
	st, err := OpenState(cfg.Dir)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := clearKeyRefused(st.db); err != nil {
		return err
	}
	if err := st.clearSignedOut(); err != nil {
		return err
	}
	*cfg = next
	return nil
}
