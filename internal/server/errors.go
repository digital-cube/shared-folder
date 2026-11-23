package server

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable reason an operation was refused. The
// HTTP API returns it as error.code; clients branch on it.
type Code string

const (
	CodeInvalidRequest Code = "invalid_request" // malformed body or parameter
	CodeInvalidPath    Code = "invalid_path"    // rejected by pathx.Normalize
	CodeCaseConflict   Code = "case_conflict"   // differs from an existing name only in letter case
	CodeStaleBase      Code = "stale_base"      // base_hash is not the current version (delete, move)
	CodeNotFound       Code = "not_found"       // the path does not exist
	CodeDestExists     Code = "dest_exists"     // move target exists with other content
	CodeKindMismatch   Code = "kind_mismatch"   // a file where a directory is expected, or the other way round
	CodeParentNotDir   Code = "parent_not_dir"  // a parent of the path is a file
	CodeIsDir          Code = "is_dir"          // delete/move of a directory (use rmdir)
	CodeDirNotEmpty    Code = "dir_not_empty"   // rmdir of a directory that still has entries
	CodeHashMismatch   Code = "hash_mismatch"   // uploaded content does not have new_hash
	CodeSizeMismatch   Code = "size_mismatch"   // uploaded content does not have the announced size
	CodeUploadMissing  Code = "upload_missing"  // commit before the content arrived in tmp/
	CodeUnknownUpload  Code = "unknown_upload"  // no such upload_id for this member
	CodeUnknownMember  Code = "unknown_member"  // no such username
	CodeMemberExists   Code = "member_exists"   // username already taken in this folder
	CodeUnauthorized   Code = "unauthorized"    // missing, unknown or revoked token
	CodeUnknownShare   Code = "unknown_share"
)

// Error is an operation refused by the rules. It never leaves the database
// or the disk changed.
type Error struct {
	Code    Code
	Message string
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
