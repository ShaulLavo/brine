package store

const dropsGatewayConfigSchema = `
CREATE TABLE drop_gateway_configs (
 revision TEXT PRIMARY KEY, config_digest TEXT NOT NULL, zone TEXT NOT NULL,
 manage_port INTEGER NOT NULL CHECK(manage_port BETWEEN 1024 AND 65535 AND manage_port<>2019),
 view_port INTEGER NOT NULL CHECK(view_port BETWEEN 1024 AND 65535 AND view_port<>2019 AND view_port<>manage_port),
 tls TEXT NOT NULL CHECK(tls='dns01-cloudflare'), file_hash TEXT NOT NULL,
 operation_id TEXT NOT NULL REFERENCES operations(id));
CREATE TRIGGER drop_gateway_configs_unique BEFORE INSERT ON drop_gateway_configs WHEN EXISTS(SELECT 1 FROM drop_gateway_configs WHERE revision=NEW.revision) BEGIN SELECT RAISE(ABORT,'retained drop gateway revision'); END;
CREATE TRIGGER drop_gateway_configs_identity BEFORE UPDATE ON drop_gateway_configs BEGIN SELECT RAISE(ABORT,'immutable drop gateway configuration'); END;
CREATE TRIGGER drop_gateway_configs_retained BEFORE DELETE ON drop_gateway_configs BEGIN SELECT RAISE(ABORT,'retained drop gateway configuration'); END;
`
