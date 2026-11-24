package server

import (
	"errors"
	"fmt"
	"time"
)

// Code is a stable, machine-readable reason an operation was refused. The
// HTTP API returns it as error.code; clients branch on it.
type Code string

const (
	CodeInvalidRequest   Code = "invalid_request" // malformed body or parameter
	CodeInvalidPath      Code = "invalid_path"    // rejected by pathx.Normalize
	CodeCaseConflict     Code = "case_conflict"   // differs from an existing name only in letter case
	CodeStaleBase        Code = "stale_base"      // base_hash is not the current version (delete, move)
	CodeNotFound         Code = "not_found"       // the path does not exist
	CodeDestExists       Code = "dest_exists"     // move target exists with other content
	CodeKindMismatch     Code = "kind_mismatch"   // a file where a directory is expected, or the other way round
	CodeParentNotDir     Code = "parent_not_dir"  // a parent of the path is a file
	CodeIsDir            Code = "is_dir"          // delete/move of a directory (use rmdir)
	CodeDirNotEmpty      Code = "dir_not_empty"   // rmdir of a directory that still has entries
	CodeHashMismatch     Code = "hash_mismatch"   // uploaded content does not have new_hash
	CodeSizeMismatch     Code = "size_mismatch"   // uploaded content does not have the announced size
	CodeUploadMissing    Code = "upload_missing"  // commit before the content arrived in tmp/
	CodeUnknownUpload    Code = "unknown_upload"  // no such upload_id for this member
	CodeUnknownMember    Code = "unknown_member"  // no such username
	CodeMemberExists     Code = "member_exists"   // username taken by an active member of this folder
	CodeLastAdmin        Code = "last_admin"      // the change would leave the folder without an active admin
	CodeUnauthorized     Code = "unauthorized"    // missing, unknown or revoked token
	CodeUnknownShare     Code = "unknown_share"
	CodeInvalidKey       Code = "invalid_key"        // not a usable OpenSSH public key
	CodeKeyInUse         Code = "key_in_use"         // the SSH key already belongs to another member or line
	CodeSSHNotConfigured Code = "ssh_not_configured" // the server manages no authorized_keys file
	CodeForbidden        Code = "forbidden"          // an admin operation called by a member without the admin role
	CodeUnknownTrash     Code = "unknown_trash"      // no such trash entry (or it was purged)
	CodeTrashDamaged     Code = "trash_damaged"      // the file of a trash entry is missing or has other content; run verify
	CodeFileChanged      Code = "file_changed"       // the file on disk is missing or does not have the hash the database records; run verify

	// Accounts (accounts.go).
	CodeInvalidCredentials     Code = "invalid_credentials"      // wrong password, unknown or disabled account: one answer for all
	CodeTooManyAttempts        Code = "too_many_attempts"        // the attempt limit of the username or the address; see Error.RetryAfter
	CodePasswordChangeRequired Code = "password_change_required" // the account has a temporary password: POST /v1/me/password first
	CodeWrongPassword          Code = "wrong_password"           // the old password of a password change is wrong
	CodeInvalidPassword        Code = "invalid_password"         // a new password breaks the rules (length, equal to the username)
	CodeUnknownUser            Code = "unknown_user"             // no such account
	CodeUserExists             Code = "user_exists"              // the username is taken by another account
	CodeUserDisabled           Code = "user_disabled"            // the account is disabled (shfd user enable)
	CodeUnknownDevice          Code = "unknown_device"           // no such device of this account
	CodeDeviceRevoked          Code = "device_revoked"           // the device's credentials for the folder were revoked: sign in again
)

// Error is an operation refused by the rules. It never leaves the database
// or the disk changed.
type Error struct {
	Code    Code
	Message string
	// RetryAfter is set with CodeTooManyAttempts: how long until the next
	// attempt is let through.
	RetryAfter time.Duration
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

func errf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the Code of err, or "" when err is not an *Error.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
