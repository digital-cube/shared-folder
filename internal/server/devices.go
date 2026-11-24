package server

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/digital-cube/shared-folder/internal/authkeys"
)

// Device credentials of a folder (docs/specifikacija.md, section 3.2): a
// device of an account that is an active member gets, with
// POST /v1/shares/{id}/devices, a folder token of its own (member_devices)
// and then registers an SSH key of its own with it. The member stays the
// identity (updated_by, the trash's "by"); the device is only a
// credential. Two devices of one member therefore never take each other's
// key, and revoking one leaves the other alone. members.token_hash and
// members.ssh_pubkey stay as the member's own, legacy credential.
//
// A folder token works while the member_devices row, the member, the
// account's device (registry.db devices: not signed out, not expired) and
// the account (users: not disabled) are all active; the API and
// rsync-gate check all four on every use.

// MemberDevice is a row of member_devices, without the token hash.
type MemberDevice struct {
	ID        int64      `json:"-"`
	MemberID  int64      `json:"-"`
	DeviceID  int64      `json:"id"`
	SSHPubkey string     `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

const memberDeviceCols = `id, member_id, device_id, ssh_pubkey, created_at, revoked_at`

func scanMemberDevice(sc interface{ Scan(...any) error }) (*MemberDevice, error) {
	var d MemberDevice
	var created, revoked sql.NullString
	if err := sc.Scan(&d.ID, &d.MemberID, &d.DeviceID, &d.SSHPubkey, &created, &revoked); err != nil {
		return nil, err
	}
	if t := parseTime(created); t != nil {
		d.CreatedAt = *t
	}
	d.RevokedAt = parseTime(revoked)
	return &d, nil
}

// MemberDevices lists the device credentials of the folder, revoked ones
// included, by member and device.
func (s *Share) MemberDevices() ([]MemberDevice, error) {
	rows, err := s.db.Query(`SELECT ` + memberDeviceCols + ` FROM member_devices ORDER BY member_id, device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemberDevice
	for rows.Next() {
		d, err := scanMemberDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// memberDevice returns the credential of one device of a member.
func (s *Share) memberDevice(memberID, deviceID int64) (*MemberDevice, error) {
	d, err := scanMemberDevice(s.db.QueryRow(`SELECT `+memberDeviceCols+` FROM member_devices WHERE member_id = ? AND device_id = ?`, memberID, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnknownDevice, "no device %d of member %d", deviceID, memberID)
	}
	return d, err
}

// authenticateFolderToken returns the member whose device's folder token
// this is, with DeviceID and FolderDevice set. It knows only this folder's
// database; Server.AuthenticateFolder checks the account and its device.
func (s *Share) authenticateFolderToken(token string) (*Member, error) {
	var devID int64
	var devRevoked sql.NullString
	m, err := scanMember(s.db.QueryRow(`SELECT m.id, m.username, m.role, m.ssh_pubkey, m.created_at, m.revoked_at, m.last_seen_at,
		m.last_push_at, m.last_seq_acked, m.user_id, d.device_id, d.revoked_at
		FROM member_devices d JOIN members m ON m.id = d.member_id WHERE d.token_hash = ?`, HashToken(token)),
		&devID, &devRevoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnauthorized, "unknown token")
	}
	if err != nil {
		return nil, err
	}
	switch {
	case m.RevokedAt != nil:
		return nil, errf(CodeUnauthorized, "token revoked")
	case devRevoked.Valid:
		return nil, errf(CodeUnauthorized, "device revoked")
	}
	m.DeviceID, m.FolderDevice = devID, true
	return m, nil
}

// issueDeviceToken makes the folder token of an account's device for an
// active member, or a new one in place of the device's earlier token,
// which stops working at once: its SSH key is dropped with it (the client
// registers one again with the new token) and its WebSockets are closed.
// A credential revoked in this folder (by an admin, or with the member) is
// not brought back: the device signs in again and is a new device.
func (s *Share) issueDeviceToken(memberID, deviceID int64) (string, error) {
	token := NewToken()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var revoked sql.NullString
	if err := tx.QueryRow(`SELECT revoked_at FROM members WHERE id = ?`, memberID).Scan(&revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errf(CodeUnknownMember, "no member %d", memberID)
		}
		return "", err
	}
	if revoked.Valid {
		return "", errf(CodeForbidden, "the member is revoked")
	}
	var id int64
	var devRevoked sql.NullString
	err = tx.QueryRow(`SELECT id, revoked_at FROM member_devices WHERE member_id = ? AND device_id = ?`, memberID, deviceID).Scan(&id, &devRevoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`INSERT INTO member_devices (member_id, device_id, token_hash, created_at) VALUES (?, ?, ?, ?)`,
			memberID, deviceID, HashToken(token), fmtTime(s.now())); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	case devRevoked.Valid:
		return "", errf(CodeDeviceRevoked, "this device's access to the folder was revoked; sign in again to get a new device")
	default:
		if _, err := tx.Exec(`UPDATE member_devices SET token_hash = ?, ssh_pubkey = '' WHERE id = ?`, HashToken(token), id); err != nil {
			return "", err
		}
		// The old key goes with the old token, while the row is locked.
		if s.keys != nil {
			if err := s.keys.RemoveDevice(s.ID, memberID, deviceID); err != nil {
				return "", fmt.Errorf("removing the device's old SSH key from %s: %w", s.keys.Path, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.revokeWatchersWhere(func(w *Watcher) bool {
		return w.MemberID == memberID && w.DeviceID == deviceID && w.FolderDevice
	})
	return token, nil
}

// setDeviceSSHKey stores the key of the device of m's folder token in
// member_devices.ssh_pubkey and writes its line to authorized_keys,
// replacing the device's earlier key. key is already parsed.
func (s *Share) setDeviceSSHKey(m *Member, key string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revoked sql.NullString
	if err := tx.QueryRow(`SELECT coalesce(d.revoked_at, m.revoked_at) FROM member_devices d JOIN members m ON m.id = d.member_id
		WHERE d.member_id = ? AND d.device_id = ?`, m.ID, m.DeviceID).Scan(&revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errf(CodeUnauthorized, "no credentials of device %d in this folder", m.DeviceID)
		}
		return err
	}
	if revoked.Valid {
		return errf(CodeUnauthorized, "device revoked")
	}
	if _, err := tx.Exec(`UPDATE member_devices SET ssh_pubkey = ? WHERE member_id = ? AND device_id = ?`, key, m.ID, m.DeviceID); err != nil {
		return err
	}
	e := authkeys.Entry{ShareID: s.ID, MemberID: m.ID, DeviceID: m.DeviceID, Username: m.Username, Device: m.DeviceName, Key: key}
	if err := s.keys.Set(e); err != nil {
		if errors.Is(err, authkeys.ErrKeyInUse) {
			return errf(CodeKeyInUse, "%v", err)
		}
		return err
	}
	return tx.Commit()
}

// RevokeMemberDevice revokes the folder credentials of one device of a
// member (DELETE /admin/members/{m}/devices/{d}): its token stops working,
// its line leaves authorized_keys and its WebSockets are closed. The
// member, its other devices and the account's device elsewhere stay.
// Revoking twice is not an error.
func (s *Share) RevokeMemberDevice(username string, deviceID int64) error {
	m, err := s.MemberByName(username)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE member_devices SET revoked_at = coalesce(revoked_at, ?) WHERE member_id = ? AND device_id = ?`,
		fmtTime(s.now()), m.ID, deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errf(CodeUnknownDevice, "member %s has no device %d in this folder", username, deviceID)
	}
	s.revokeWatchersWhere(func(w *Watcher) bool {
		return w.MemberID == m.ID && w.DeviceID == deviceID && w.FolderDevice
	})
	if s.keys != nil {
		if err := s.keys.RemoveDevice(s.ID, m.ID, deviceID); err != nil {
			return fmt.Errorf("the device is revoked, but removing its SSH key from %s failed: %w", s.keys.Path, err)
		}
	}
	return nil
}

// revokeDevices revokes, in this folder, the credentials of the given
// account devices (signed out, revoked or the account disabled) and closes
// every WebSocket they opened, with a folder token or the account's device
// token. It returns how many credentials were active.
func (s *Share) revokeDevices(deviceIDs []int64) (int, error) {
	if len(deviceIDs) == 0 {
		return 0, nil
	}
	ids := map[int64]bool{}
	args := []any{fmtTime(s.now())}
	for _, d := range deviceIDs {
		ids[d] = true
		args = append(args, d)
	}
	rows, err := s.db.Query(`UPDATE member_devices SET revoked_at = ? WHERE revoked_at IS NULL AND device_id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(deviceIDs)), ",")+`) RETURNING member_id, device_id`, args...)
	if err != nil {
		return 0, err
	}
	type cred struct{ member, device int64 }
	var done []cred
	for rows.Next() {
		var c cred
		if err := rows.Scan(&c.member, &c.device); err != nil {
			rows.Close()
			return 0, err
		}
		done = append(done, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	s.revokeWatchersWhere(func(w *Watcher) bool { return ids[w.DeviceID] })
	if s.keys == nil {
		return len(done), nil
	}
	var errs []error
	for _, c := range done {
		errs = append(errs, s.keys.RemoveDevice(s.ID, c.member, c.device))
	}
	if err := errors.Join(errs...); err != nil {
		return len(done), fmt.Errorf("folder %s: the devices are revoked, but removing their SSH keys from %s failed: %w", s.ID, s.keys.Path, err)
	}
	return len(done), nil
}

// ---- the server's side: accounts and their devices ------------------------

// deviceState is an account's device as the checks of a folder credential
// see it.
type deviceState struct {
	Device
	UserID       int64
	UserDisabled bool
}

func (s *Server) deviceStates() (map[int64]deviceState, error) {
	rows, err := s.reg.Query(`SELECT d.id, d.user_id, d.name, d.kind, d.created_at, d.last_seen_at, d.expires_at, d.revoked_at, u.disabled_at
		FROM devices d JOIN users u ON u.id = d.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]deviceState{}
	for rows.Next() {
		var d deviceState
		var created, seen, expires, revoked, disabled sql.NullString
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.Kind, &created, &seen, &expires, &revoked, &disabled); err != nil {
			return nil, err
		}
		if t := parseTime(created); t != nil {
			d.CreatedAt = *t
		}
		d.LastSeenAt, d.ExpiresAt, d.RevokedAt = parseTime(seen), parseTime(expires), parseTime(revoked)
		d.UserDisabled = disabled.Valid
		out[d.ID] = d
	}
	return out, rows.Err()
}

// deviceState reads one device of the registry.
func (s *Server) deviceState(deviceID int64) (*deviceState, error) {
	var d deviceState
	var created, seen, expires, revoked, disabled sql.NullString
	err := s.reg.QueryRow(`SELECT d.id, d.user_id, d.name, d.kind, d.created_at, d.last_seen_at, d.expires_at, d.revoked_at, u.disabled_at
		FROM devices d JOIN users u ON u.id = d.user_id WHERE d.id = ?`, deviceID).
		Scan(&d.ID, &d.UserID, &d.Name, &d.Kind, &created, &seen, &expires, &revoked, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errf(CodeUnknownDevice, "no device %d", deviceID)
	}
	if err != nil {
		return nil, err
	}
	if t := parseTime(created); t != nil {
		d.CreatedAt = *t
	}
	d.LastSeenAt, d.ExpiresAt, d.RevokedAt = parseTime(seen), parseTime(expires), parseTime(revoked)
	d.UserDisabled = disabled.Valid
	return &d, nil
}

// why is the reason a device's credential does not work at now, "" when
// it does: the member must be linked to the device's account, which must
// be enabled, and the device must be neither signed out nor expired.
func (d *deviceState) why(m *Member, now time.Time) string {
	switch {
	case m.UserID == nil || *m.UserID != d.UserID:
		return "the device belongs to another account"
	case d.UserDisabled:
		return "account disabled"
	case d.RevokedAt != nil:
		return "device signed out"
	case !d.Active(now):
		return "session expired"
	}
	return ""
}

// CheckFolderDevice says whether device deviceID of member m may use its
// folder credentials: the member and its member_devices row active, the
// account's device neither signed out nor expired, the account enabled.
// It returns the device's name, when the device exists, also with a
// refusal. Every refusal is CodeDeviceRevoked, with the reason in the
// message. rsync-gate --device calls it for every transfer.
func (s *Server) CheckFolderDevice(sh *Share, m *Member, deviceID int64) (string, error) {
	d, err := s.deviceState(deviceID)
	if err != nil && CodeOf(err) != CodeUnknownDevice {
		return "", err
	}
	name := ""
	if d != nil {
		name = d.Name
	}
	if m.RevokedAt != nil {
		return name, errf(CodeDeviceRevoked, "member %s is revoked", m.Username)
	}
	md, err := sh.memberDevice(m.ID, deviceID)
	if CodeOf(err) == CodeUnknownDevice {
		return name, errf(CodeDeviceRevoked, "device %d has no credentials in this folder", deviceID)
	}
	if err != nil {
		return name, err
	}
	if md.RevokedAt != nil {
		return name, errf(CodeDeviceRevoked, "device %d is revoked in this folder", deviceID)
	}
	if d == nil {
		return "", errf(CodeDeviceRevoked, "device %d does not exist", deviceID)
	}
	if why := d.why(m, s.opts.Now()); why != "" {
		return name, errf(CodeDeviceRevoked, "device %d: %s", deviceID, why)
	}
	return name, nil
}

// AuthenticateFolder resolves the token of a request on a route of sh: the
// member's own token, a device's folder token (member_devices) or an
// account's device token ("shd_…") of an active member. The member comes
// back with the credential's device set (Member.DeviceID). A folder token
// whose member, device or account is no longer active, and an unknown
// token, fail with CodeUnauthorized; an account with a temporary password
// fails with CodePasswordChangeRequired on its device token (its folder
// tokens keep working, like the member's own token).
func (s *Server) AuthenticateFolder(sh *Share, token string) (*Member, error) {
	if token == "" {
		return nil, errf(CodeUnauthorized, "missing token")
	}
	if IsDeviceToken(token) {
		sess, err := s.AuthenticateDevice(token)
		if err != nil {
			return nil, err
		}
		if sess.User.MustChange {
			return nil, errf(CodePasswordChangeRequired, "the account has a temporary password; change it first (POST /v1/me/password)")
		}
		m, err := s.MemberForUser(sh, sess.User.ID)
		if err != nil {
			return nil, err
		}
		m.DeviceID = sess.DeviceID
		return m, nil
	}
	m, err := sh.Authenticate(token)
	if err == nil || err.Error() != errf(CodeUnauthorized, "unknown token").Error() {
		return m, err
	}
	m, err = sh.authenticateFolderToken(token)
	if err != nil {
		return nil, err
	}
	d, err := s.deviceState(m.DeviceID)
	if CodeOf(err) == CodeUnknownDevice {
		return nil, errf(CodeUnauthorized, "device revoked")
	}
	if err != nil {
		return nil, err
	}
	now := s.opts.Now()
	if why := d.why(m, now); why != "" {
		return nil, errf(CodeUnauthorized, "device revoked: %s", why)
	}
	m.DeviceName = d.Name
	if d.LastSeenAt == nil || now.Sub(*d.LastSeenAt) >= touchEvery {
		// The device was seen; a GUI session's expiry is not slid by a
		// folder token.
		if _, err := s.reg.Exec(`UPDATE devices SET last_seen_at = ? WHERE id = ?`, fmtTime(now), d.ID); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// FolderDevice answers POST /v1/shares/{id}/devices: the folder token of
// the device (shown only now; only its hash is stored), the member it acts
// as and where rsync over SSH connects.
type FolderDevice struct {
	Token    string      `json:"token"`
	Member   string      `json:"member"`
	Role     string      `json:"role"`
	DeviceID int64       `json:"device_id"`
	SSH      SSHEndpoint `json:"ssh"`
}

// IssueFolderDevice makes the folder token of the session's device in sh,
// or a new one in place of its earlier token (see Share.issueDeviceToken).
// An account that is not an active member of the folder is refused with
// CodeForbidden.
func (s *Server) IssueFolderDevice(sh *Share, sess *Session) (*FolderDevice, error) {
	m, err := s.MemberForUser(sh, sess.User.ID)
	if CodeOf(err) == CodeUnauthorized {
		return nil, errf(CodeForbidden, "the account is not a member of this folder")
	}
	if err != nil {
		return nil, err
	}
	token, err := sh.issueDeviceToken(m.ID, sess.DeviceID)
	if err != nil {
		return nil, err
	}
	return &FolderDevice{Token: token, Member: m.Username, Role: m.Role, DeviceID: sess.DeviceID, SSH: s.opts.SSH}, nil
}

// revokeDeviceCredentials revokes the folder credentials of the given
// account devices in every folder (a device signed out or revoked, an
// account disabled) and removes their lines from authorized_keys. It goes
// through every folder rather than user_shares, which is only an index.
func (s *Server) revokeDeviceCredentials(deviceIDs []int64) (int, error) {
	if len(deviceIDs) == 0 {
		return 0, nil
	}
	shares, err := s.Shares()
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, si := range shares {
		sh, err := s.Share(si.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		k, err := sh.revokeDevices(deviceIDs)
		n += k
		errs = append(errs, err)
	}
	return n, errors.Join(errs...)
}

// MemberDeviceInfo is one device of a member as GET /admin/members and
// shfd member list show it.
type MemberDeviceInfo struct {
	ID             int64      `json:"id"` // registry.db devices.id
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	SSHFingerprint string     `json:"ssh_fingerprint"` // "" until the device registers a key
	CreatedAt      time.Time  `json:"created_at"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
	// Revoked: the credential no longer works, because it was revoked in
	// this folder (RevokedAt) or the device was signed out, expired or its
	// account disabled.
	Revoked   bool       `json:"revoked"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	Connected int        `json:"connected"`
}
