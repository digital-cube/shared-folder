-- Credentials of an account's device for this folder (docs/specifikacija.md,
-- section 3.2): a folder token of its own and an SSH key of its own, so two
-- devices of one member do not take each other's key. device_id is
-- registry.db devices.id. The member stays the identity in the history;
-- members.token_hash and members.ssh_pubkey remain the legacy credential.
CREATE TABLE member_devices (
    id          INTEGER PRIMARY KEY,
    member_id   INTEGER NOT NULL REFERENCES members (id),
    device_id   INTEGER NOT NULL,
    token_hash  TEXT NOT NULL UNIQUE,      -- hex SHA-256 of the folder token
    ssh_pubkey  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    revoked_at  TEXT,
    UNIQUE (member_id, device_id)
);
CREATE INDEX member_devices_device ON member_devices (device_id);
