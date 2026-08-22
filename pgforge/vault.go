package main

// Encrypted secrets vault, usable from SQL, edge functions and cron jobs.
// Values are pgp_sym encrypted with a per-database random key; the key table
// is owned by the superuser with all access revoked, and reads/writes go only
// through SECURITY DEFINER functions granted to the project role. This is the
// same at-rest model as the well-known Postgres vault extensions: it protects
// secrets from being casually read through API roles or careless SELECTs,
// while the owner keeps full control.
//
//	SELECT forgebase.secret_set('stripe_key', 'sk_live_...');
//	SELECT forgebase.secret_get('stripe_key');
//	SELECT name, created_at FROM forgebase.secret_list();
//	SELECT forgebase.secret_delete('stripe_key');

import (
	"net/http"

	"github.com/lib/pq"
)

const vaultFns = `
CREATE SCHEMA IF NOT EXISTS forgebase;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TABLE IF NOT EXISTS forgebase.vault_key (
	id boolean PRIMARY KEY DEFAULT true CHECK (id),
	key text NOT NULL
);
INSERT INTO forgebase.vault_key (key)
	SELECT encode(gen_random_bytes(32), 'hex')
	WHERE NOT EXISTS (SELECT 1 FROM forgebase.vault_key);
REVOKE ALL ON forgebase.vault_key FROM PUBLIC;
CREATE TABLE IF NOT EXISTS forgebase.vault_secrets (
	name text PRIMARY KEY,
	value_enc bytea NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON forgebase.vault_secrets FROM PUBLIC;
CREATE OR REPLACE FUNCTION forgebase.secret_set(sname text, svalue text) RETURNS void
SECURITY DEFINER SET search_path = forgebase, pg_temp AS $fb$
	INSERT INTO forgebase.vault_secrets (name, value_enc)
	VALUES (sname, pgp_sym_encrypt(svalue, (SELECT key FROM forgebase.vault_key)))
	ON CONFLICT (name) DO UPDATE
		SET value_enc = pgp_sym_encrypt(svalue, (SELECT key FROM forgebase.vault_key)),
		    updated_at = now();
$fb$ LANGUAGE sql;
CREATE OR REPLACE FUNCTION forgebase.secret_get(sname text) RETURNS text
SECURITY DEFINER SET search_path = forgebase, pg_temp AS $fb$
	SELECT pgp_sym_decrypt(value_enc, (SELECT key FROM forgebase.vault_key))
	FROM forgebase.vault_secrets WHERE name = sname;
$fb$ LANGUAGE sql;
CREATE OR REPLACE FUNCTION forgebase.secret_delete(sname text) RETURNS boolean
SECURITY DEFINER SET search_path = forgebase, pg_temp AS $fb$
	WITH del AS (DELETE FROM forgebase.vault_secrets WHERE name = sname RETURNING 1)
	SELECT count(*) > 0 FROM del;
$fb$ LANGUAGE sql;
CREATE OR REPLACE FUNCTION forgebase.secret_list()
RETURNS TABLE(name text, created_at timestamptz, updated_at timestamptz)
SECURITY DEFINER SET search_path = forgebase, pg_temp AS $fb$
	SELECT name, created_at, updated_at FROM forgebase.vault_secrets ORDER BY name;
$fb$ LANGUAGE sql;
`

// ensureVault installs the vault and grants the project role function access
// (never table access - the definer functions are the only doorway).
func (a *app) ensureVault(slug string) error {
	db, err := a.dbFor(slug)
	if err != nil {
		return err
	}
	if _, err := db.Exec(vaultFns); err != nil {
		return err
	}
	role := a.roleFor(slug)
	for _, g := range []string{
		`GRANT USAGE ON SCHEMA forgebase TO ` + pq.QuoteIdentifier(role),
		`GRANT EXECUTE ON FUNCTION forgebase.secret_set(text, text) TO ` + pq.QuoteIdentifier(role),
		`GRANT EXECUTE ON FUNCTION forgebase.secret_get(text) TO ` + pq.QuoteIdentifier(role),
		`GRANT EXECUTE ON FUNCTION forgebase.secret_delete(text) TO ` + pq.QuoteIdentifier(role),
		`GRANT EXECUTE ON FUNCTION forgebase.secret_list() TO ` + pq.QuoteIdentifier(role),
	} {
		db.Exec(g)
	}
	return nil
}

// vaultEnable is the panel entry point (Settings page button).
func (a *app) vaultEnable(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	back := "/p/" + slug + "/settings"
	if err := a.ensureVault(slug); err != nil {
		redirectErr(w, r, back, "Vault setup failed: "+err.Error())
		return
	}
	a.audit(r, "vault-enable", slug)
	redirectMsg(w, r, back, "Vault ready: forgebase.secret_set / secret_get / secret_list / secret_delete are live for your role.")
}
