-- Optional OIDC single sign-on: federated users + the single-row IdP
-- configuration edited from Settings -> Authentication. OIDC is default-off;
-- local (password) accounts keep working regardless of this table's state.

-- Federated users have no password; identity is the immutable OIDC subject.
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE users
    ADD COLUMN auth_source  text NOT NULL DEFAULT 'local'
        CHECK (auth_source IN ('local', 'oidc')),
    ADD COLUMN oidc_subject text;
CREATE UNIQUE INDEX users_oidc_subject_idx
    ON users (oidc_subject) WHERE oidc_subject IS NOT NULL;
-- Exactly one credential shape per source: local rows keep a hash and no
-- subject, oidc rows the reverse. A row violating this is a server bug and
-- must be loud.
ALTER TABLE users ADD CONSTRAINT users_auth_shape CHECK (
    (auth_source = 'local' AND password_hash IS NOT NULL AND oidc_subject IS NULL)
 OR (auth_source = 'oidc'  AND password_hash IS NULL     AND oidc_subject IS NOT NULL)
);

-- Single-row IdP configuration (same always-true-bool PK trick as
-- dashboard_settings). The CHECK mirrors httpapi validation: enabled
-- requires the fields the authorization-code flow cannot run without.
CREATE TABLE oidc_settings (
    id             boolean PRIMARY KEY DEFAULT true CHECK (id),
    enabled        boolean NOT NULL DEFAULT false,
    issuer         text    NOT NULL DEFAULT '',
    client_id      text    NOT NULL DEFAULT '',
    client_secret  text    NOT NULL DEFAULT '',
    redirect_url   text    NOT NULL DEFAULT '',
    scopes         text[]  NOT NULL DEFAULT '{openid,profile,email}',
    username_claim text    NOT NULL DEFAULT 'preferred_username',
    role_claim     text    NOT NULL DEFAULT 'groups',
    admin_values   text[]  NOT NULL DEFAULT '{}',
    -- PEM blob, not a file path: the server container filesystem is
    -- immutable, and DB-stored config must be self-contained.
    ca_pem         text    NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     text    NOT NULL DEFAULT '',
    CHECK (NOT enabled OR (issuer <> '' AND client_id <> '' AND client_secret <> '' AND redirect_url <> ''))
);
INSERT INTO oidc_settings DEFAULT VALUES;
