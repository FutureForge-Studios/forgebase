-- ForgeBase first-boot init: metadata database + cluster hardening.
-- Runs once via /docker-entrypoint-initdb.d against the default 'postgres' DB
-- as the postgres superuser.

CREATE DATABASE pgforge;

-- projects must not be able to connect anywhere but their own database
REVOKE CONNECT ON DATABASE postgres  FROM PUBLIC;
REVOKE CONNECT ON DATABASE pgforge   FROM PUBLIC;
REVOKE CONNECT ON DATABASE template1 FROM PUBLIC;

\connect pgforge

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ------------------------------------------------------------------ metadata
CREATE TABLE projects (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug         text UNIQUE NOT NULL,
  role_name    text NOT NULL,
  password_enc bytea NOT NULL,           -- pgp_sym_encrypt(password, session key)
  status       text NOT NULL DEFAULT 'active',  -- active | paused
  created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
  id     bigserial PRIMARY KEY,
  at     timestamptz NOT NULL DEFAULT now(),
  actor  text NOT NULL,
  action text NOT NULL,
  detail jsonb
);

CREATE TABLE settings (
  key   text PRIMARY KEY,
  value text NOT NULL
);

-- schema-now, features-later (orgs/teams/API keys per the architecture doc)
CREATE TABLE orgs (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text UNIQUE NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid REFERENCES projects(id) ON DELETE CASCADE,
  name       text NOT NULL,
  kind       text NOT NULL,              -- anon | service
  secret_enc bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE jwt_keys (
  project_id uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  secret_enc bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
