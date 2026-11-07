package store

const dropsSchema = `
CREATE TABLE drop_plans (
 id TEXT PRIMARY KEY, kind TEXT NOT NULL CHECK(kind IN ('upload','apply')),
 requester TEXT NOT NULL, action TEXT NOT NULL,
 resource_class TEXT NOT NULL CHECK(resource_class IN ('drop-local','global-gateway')),
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536));
CREATE TRIGGER drop_plans_immutable BEFORE UPDATE ON drop_plans BEGIN SELECT RAISE(ABORT,'immutable drop plan'); END;
CREATE TRIGGER drop_plans_retained BEFORE DELETE ON drop_plans BEGIN SELECT RAISE(ABORT,'retained drop plan'); END;

CREATE TABLE drop_uploads (
 id TEXT PRIMARY KEY REFERENCES operations(id), plan_id TEXT NOT NULL REFERENCES drop_plans(id),
 state TEXT NOT NULL CHECK(state IN ('awaiting_input','prepared','reaping','reaped')),
 version INTEGER NOT NULL CHECK(version>0), committed_length INTEGER NOT NULL CHECK(committed_length>=0),
 expires_ns INTEGER NOT NULL, pin_until_ns INTEGER NOT NULL,
 preparation BLOB CHECK(length(preparation)<=8192), preparation_digest TEXT,
 consumer_operation_id TEXT UNIQUE REFERENCES operations(id),
 CHECK((state='awaiting_input')=(preparation IS NULL) OR state IN ('reaping','reaped')),
 CHECK((preparation IS NULL)=(preparation_digest IS NULL)));
CREATE TRIGGER drop_upload_identity BEFORE UPDATE OF id,plan_id,expires_ns ON drop_uploads BEGIN SELECT RAISE(ABORT,'immutable drop upload'); END;
CREATE TRIGGER drop_upload_seal BEFORE UPDATE OF preparation,preparation_digest ON drop_uploads WHEN OLD.preparation IS NOT NULL BEGIN SELECT RAISE(ABORT,'immutable drop preparation'); END;
CREATE TRIGGER drop_upload_consumer BEFORE UPDATE OF consumer_operation_id ON drop_uploads WHEN OLD.consumer_operation_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'immutable drop consumer'); END;
CREATE TRIGGER drop_upload_retained BEFORE DELETE ON drop_uploads BEGIN SELECT RAISE(ABORT,'retained drop upload'); END;
CREATE TRIGGER drop_upload_version BEFORE UPDATE ON drop_uploads WHEN NEW.version<>OLD.version+1 OR NEW.committed_length<OLD.committed_length OR NEW.pin_until_ns<OLD.pin_until_ns BEGIN SELECT RAISE(ABORT,'stale drop upload'); END;
CREATE TRIGGER drop_upload_transition BEFORE UPDATE OF state ON drop_uploads WHEN NEW.state<>OLD.state AND NOT (
 (OLD.state='awaiting_input' AND NEW.state IN ('prepared','reaping')) OR
 (OLD.state='prepared' AND NEW.state='reaping') OR (OLD.state='reaping' AND NEW.state='reaped')) BEGIN SELECT RAISE(ABORT,'illegal drop upload transition'); END;

CREATE TABLE drop_incarnations (
 id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE REFERENCES drop_names(name) DEFERRABLE INITIALLY DEFERRED,
 upload_id TEXT NOT NULL UNIQUE REFERENCES drop_uploads(id),
 state TEXT NOT NULL CHECK(state IN ('planned','live','disposing','disposed','abandoned','suspended')),
 state_version INTEGER NOT NULL CHECK(state_version>0),
 visibility TEXT NOT NULL CHECK(visibility IN ('private','public')), kind TEXT NOT NULL CHECK(kind='static'),
 created_ns INTEGER NOT NULL, window_ns INTEGER NOT NULL, last_visit_ns INTEGER,
 pinned INTEGER NOT NULL CHECK(pinned IN (0,1)), ttl_ns INTEGER NOT NULL CHECK(ttl_ns>0),
 expanded_bytes INTEGER NOT NULL CHECK(expanded_bytes BETWEEN 0 AND 67108864),
 files INTEGER NOT NULL CHECK(files BETWEEN 0 AND 10000));
CREATE TABLE drop_names (
 name TEXT PRIMARY KEY, incarnation_id TEXT NOT NULL UNIQUE REFERENCES drop_incarnations(id) DEFERRABLE INITIALLY DEFERRED,
 state TEXT NOT NULL CHECK(state IN ('planned','live','disposing','disposed','abandoned','suspended')));
CREATE TRIGGER drop_names_retained BEFORE DELETE ON drop_names BEGIN SELECT RAISE(ABORT,'permanently burned drop name'); END;
CREATE TRIGGER drop_names_identity BEFORE UPDATE OF name,incarnation_id ON drop_names BEGIN SELECT RAISE(ABORT,'immutable drop name'); END;
CREATE TRIGGER drop_names_state BEFORE UPDATE OF state ON drop_names WHEN NOT EXISTS(SELECT 1 FROM drop_incarnations WHERE id=OLD.incarnation_id AND name=OLD.name AND state=NEW.state) BEGIN SELECT RAISE(ABORT,'drop name state mismatch'); END;
CREATE TRIGGER drop_incarnations_retained BEFORE DELETE ON drop_incarnations BEGIN SELECT RAISE(ABORT,'retained drop incarnation'); END;
CREATE TRIGGER drop_incarnations_identity BEFORE UPDATE OF id,name,upload_id,created_ns,kind,expanded_bytes,files,ttl_ns ON drop_incarnations BEGIN SELECT RAISE(ABORT,'immutable drop incarnation'); END;
CREATE TRIGGER drop_incarnations_version BEFORE UPDATE ON drop_incarnations WHEN (NEW.state_version<>OLD.state_version+1 AND NOT (NEW.state_version=OLD.state_version AND NEW.state=OLD.state AND NEW.visibility=OLD.visibility AND NEW.pinned=OLD.pinned AND NEW.window_ns=OLD.window_ns)) OR (NEW.last_visit_ns IS NOT NULL AND NEW.last_visit_ns<NEW.created_ns) OR NEW.window_ns<OLD.window_ns OR (OLD.last_visit_ns IS NOT NULL AND (NEW.last_visit_ns IS NULL OR NEW.last_visit_ns<OLD.last_visit_ns)) OR (OLD.visibility='public' AND NEW.visibility<>'public') OR (OLD.visibility<>NEW.visibility AND (OLD.state<>'live' OR NEW.state<>'live')) OR (OLD.state IN ('disposed','abandoned')) BEGIN SELECT RAISE(ABORT,'stale or terminal drop incarnation'); END;
CREATE TRIGGER drop_incarnations_transition BEFORE UPDATE OF state ON drop_incarnations WHEN OLD.state<>NEW.state AND NOT (
 (OLD.state='planned' AND NEW.state IN ('live','abandoned')) OR
 (OLD.state='live' AND NEW.state IN ('disposing','suspended')) OR
 (OLD.state='suspended' AND NEW.state='disposing') OR
 (OLD.state='disposing' AND NEW.state='disposed')) BEGIN SELECT RAISE(ABORT,'illegal drop state transition'); END;
CREATE TRIGGER drop_incarnations_name_state AFTER UPDATE OF state ON drop_incarnations BEGIN UPDATE drop_names SET state=NEW.state WHERE name=NEW.name AND incarnation_id=NEW.id; END;

CREATE TABLE drop_reservations (
 upload_id TEXT PRIMARY KEY REFERENCES drop_uploads(id), filesystem TEXT NOT NULL,
 bytes INTEGER NOT NULL CHECK(bytes>0), inodes INTEGER NOT NULL CHECK(inodes>0),
 released INTEGER NOT NULL DEFAULT 0 CHECK(released IN (0,1)));
CREATE INDEX drop_reservation_filesystem ON drop_reservations(filesystem) WHERE released=0;
CREATE TRIGGER drop_reservations_identity BEFORE UPDATE OF upload_id,filesystem,bytes,inodes ON drop_reservations BEGIN SELECT RAISE(ABORT,'immutable drop reservation'); END;
CREATE TRIGGER drop_reservations_retained BEFORE DELETE ON drop_reservations BEGIN SELECT RAISE(ABORT,'retained drop reservation'); END;
CREATE TRIGGER drop_reservations_release BEFORE UPDATE OF released ON drop_reservations WHEN OLD.released<>0 OR NEW.released<>1 OR NOT EXISTS(SELECT 1 FROM drop_uploads WHERE id=OLD.upload_id AND state='reaped' AND consumer_operation_id IS NULL) AND NOT EXISTS(SELECT 1 FROM drop_incarnations d JOIN drop_disposals x ON x.incarnation_id=d.id WHERE d.upload_id=OLD.upload_id AND d.state='disposed' AND EXISTS(SELECT 1 FROM drop_effects e WHERE e.attempt_operation_id=x.attempt_operation_id AND e.step='absence') AND EXISTS(SELECT 1 FROM drop_effects e WHERE e.attempt_operation_id IN (SELECT a.operation_id FROM drop_attempts a WHERE a.attempt_operation_id=x.attempt_operation_id) AND e.step='drain')) BEGIN SELECT RAISE(ABORT,'unsettled drop reservation'); END;

CREATE TABLE drop_grants (
 id TEXT PRIMARY KEY, incarnation_id TEXT NOT NULL REFERENCES drop_incarnations(id),
 kind TEXT NOT NULL CHECK(kind IN ('owner','view')), token_sha256 TEXT NOT NULL UNIQUE,
 requester TEXT NOT NULL, expires_ns INTEGER NOT NULL,
 operation_id TEXT NOT NULL REFERENCES operations(id),
 revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1)), consumed_ns INTEGER,
 CHECK(length(token_sha256)=71 AND substr(token_sha256,1,7)='sha256:' AND substr(token_sha256,8) NOT GLOB '*[^0-9a-f]*'));
CREATE UNIQUE INDEX drop_one_grant_per_operation ON drop_grants(operation_id);
CREATE TRIGGER drop_grants_identity BEFORE UPDATE OF id,incarnation_id,kind,token_sha256,requester,expires_ns,operation_id ON drop_grants BEGIN SELECT RAISE(ABORT,'immutable drop grant'); END;
CREATE TRIGGER drop_grants_retained BEFORE DELETE ON drop_grants BEGIN SELECT RAISE(ABORT,'retained drop grant'); END;
CREATE TRIGGER drop_grants_monotone BEFORE UPDATE ON drop_grants WHEN OLD.revoked>NEW.revoked OR OLD.consumed_ns IS NOT NULL AND NEW.consumed_ns IS NOT OLD.consumed_ns BEGIN SELECT RAISE(ABORT,'terminal drop grant'); END;

CREATE TABLE drop_gateway_heads (
 generation INTEGER PRIMARY KEY CHECK(generation>0), digest TEXT NOT NULL, revision TEXT NOT NULL,
 operation_id TEXT NOT NULL REFERENCES operations(id),
 state TEXT NOT NULL CHECK(state IN ('enabling','enabled','disabling','disabled','recovery_required')),
 ack_boot_id TEXT, ack_digest TEXT,
 CHECK((ack_boot_id IS NULL)=(ack_digest IS NULL)), CHECK(ack_digest IS NULL OR ack_digest=digest));
CREATE TRIGGER drop_gateway_heads_identity BEFORE UPDATE OF generation,digest,revision,operation_id,state ON drop_gateway_heads BEGIN SELECT RAISE(ABORT,'immutable drop gateway head'); END;
CREATE TRIGGER drop_gateway_heads_retained BEFORE DELETE ON drop_gateway_heads BEGIN SELECT RAISE(ABORT,'retained drop gateway head'); END;
CREATE TRIGGER drop_gateway_heads_order BEFORE INSERT ON drop_gateway_heads WHEN NEW.generation<>COALESCE((SELECT MAX(generation)+1 FROM drop_gateway_heads),1) BEGIN SELECT RAISE(ABORT,'nonmonotone drop gateway generation'); END;

CREATE TABLE drop_attempts (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id), plan_id TEXT NOT NULL REFERENCES drop_plans(id),
 attempt_operation_id TEXT NOT NULL REFERENCES operations(id), recovery_of TEXT NOT NULL,
 resource_class TEXT NOT NULL CHECK(resource_class IN ('drop-local','global-gateway')),
 is_resolver INTEGER NOT NULL CHECK(is_resolver IN (0,1)), active INTEGER NOT NULL CHECK(active IN (0,1)),
 admitted INTEGER NOT NULL DEFAULT 0 CHECK(admitted IN (0,1)),
 is_upload INTEGER NOT NULL CHECK(is_upload IN (0,1)),
 mutation_version INTEGER CHECK(mutation_version>0), mutation_incarnation TEXT REFERENCES drop_incarnations(id),
 mutation_state TEXT CHECK(mutation_state IN ('planned','live','disposing','disposed','abandoned','suspended')),
 mutation_visibility TEXT CHECK(mutation_visibility IN ('private','public')), mutation_expires_ns INTEGER,
 CHECK((mutation_version IS NULL)=(mutation_incarnation IS NULL)),
 CHECK((mutation_version IS NULL)=(mutation_state IS NULL)),
 CHECK((mutation_version IS NULL)=(mutation_visibility IS NULL)),
 CHECK(mutation_version IS NOT NULL OR mutation_expires_ns IS NULL));
CREATE UNIQUE INDEX drop_one_initial_admission ON drop_attempts(plan_id) WHERE admitted=1 AND is_resolver=0 AND is_upload=0;
CREATE UNIQUE INDEX drop_one_family_mutation ON drop_attempts(attempt_operation_id) WHERE mutation_version IS NOT NULL;
CREATE TRIGGER drop_attempt_mutation BEFORE UPDATE OF mutation_version,mutation_incarnation,mutation_state,mutation_visibility,mutation_expires_ns ON drop_attempts WHEN OLD.mutation_version IS NOT NULL BEGIN SELECT RAISE(ABORT,'immutable drop task mutation'); END;
CREATE UNIQUE INDEX drop_active_resolver ON drop_attempts(attempt_operation_id) WHERE is_resolver=1 AND active=1;
CREATE TRIGGER drop_attempts_identity BEFORE UPDATE OF operation_id,plan_id,attempt_operation_id,recovery_of,resource_class,is_resolver,is_upload ON drop_attempts BEGIN SELECT RAISE(ABORT,'immutable drop task join'); END;
CREATE TRIGGER drop_attempts_retained BEFORE DELETE ON drop_attempts BEGIN SELECT RAISE(ABORT,'retained drop task join'); END;
CREATE TRIGGER drop_attempts_monotone BEFORE UPDATE ON drop_attempts WHEN OLD.admitted>NEW.admitted OR OLD.active<NEW.active BEGIN SELECT RAISE(ABORT,'terminal drop attempt'); END;
CREATE TRIGGER drop_attempt_input BEFORE INSERT ON drop_attempts WHEN NOT EXISTS (
 SELECT 1 FROM operations o JOIN drop_plans p ON p.id=o.secret_ref
 WHERE o.id=NEW.operation_id AND p.id=NEW.plan_id AND p.requester=o.requester AND p.resource_class=NEW.resource_class
 AND NEW.recovery_of=o.recovery_of AND NEW.is_resolver=(o.kind='drop_resolve') AND NEW.is_upload=(p.kind='upload')
 AND ((o.kind='drop_upload_prepare' AND p.kind='upload') OR (o.kind='drop_apply' AND p.kind='apply') OR (o.kind='drop_resolve'))
 AND ((o.kind<>'drop_resolve' AND NEW.attempt_operation_id=o.id) OR (o.kind='drop_resolve' AND EXISTS (
 SELECT 1 FROM operations source JOIN drop_attempts a ON a.operation_id=source.id JOIN drop_attempts root ON root.operation_id=a.attempt_operation_id
 WHERE source.id=o.recovery_of AND source.kind IN ('drop_upload_prepare','drop_apply','drop_resolve') AND source.secret_ref=o.secret_ref AND source.requester=o.requester AND source.state='recovery_required' AND root.admitted=1 AND NEW.attempt_operation_id=root.operation_id))))
 OR EXISTS(SELECT 1 FROM drop_attempts a JOIN operations o ON o.id=a.operation_id WHERE a.attempt_operation_id=NEW.attempt_operation_id AND o.state='succeeded')
 BEGIN SELECT RAISE(ABORT,'drop operation input or family mismatch'); END;
`

// dropsExecutionSchema extends the local v36 candidate, not the frozen v35.
const dropsExecutionSchema = `
CREATE TABLE drop_materialized_usage(upload_id TEXT PRIMARY KEY REFERENCES drop_uploads(id),bytes INTEGER NOT NULL CHECK(bytes>=0),inodes INTEGER NOT NULL CHECK(inodes>0));
CREATE TRIGGER drop_materialized_usage_bound BEFORE INSERT ON drop_materialized_usage WHEN NOT EXISTS(SELECT 1 FROM drop_reservations r JOIN drop_uploads u ON u.id=r.upload_id WHERE r.upload_id=NEW.upload_id AND r.released=0 AND u.state='prepared' AND NEW.bytes<=r.bytes AND NEW.inodes<=r.inodes) BEGIN SELECT RAISE(ABORT,'unqualified materialized usage'); END;
CREATE TRIGGER drop_materialized_usage_immutable BEFORE UPDATE ON drop_materialized_usage BEGIN SELECT RAISE(ABORT,'immutable materialized usage'); END;
CREATE TRIGGER drop_materialized_usage_retained BEFORE DELETE ON drop_materialized_usage BEGIN SELECT RAISE(ABORT,'retained materialized usage'); END;
CREATE TABLE drop_reaps(upload_id TEXT PRIMARY KEY REFERENCES drop_uploads(id),attempt_operation_id TEXT NOT NULL REFERENCES operations(id));
CREATE TRIGGER drop_reaps_immutable BEFORE UPDATE ON drop_reaps BEGIN SELECT RAISE(ABORT,'immutable drop reap'); END;
CREATE TRIGGER drop_reaps_retained BEFORE DELETE ON drop_reaps BEGIN SELECT RAISE(ABORT,'retained drop reap'); END;
CREATE TABLE drop_disposals (
 attempt_operation_id TEXT PRIMARY KEY REFERENCES operations(id),incarnation_id TEXT NOT NULL REFERENCES drop_incarnations(id),deny_version INTEGER NOT NULL CHECK(deny_version>0));
CREATE TRIGGER drop_disposals_immutable BEFORE UPDATE ON drop_disposals BEGIN SELECT RAISE(ABORT,'immutable disposal denial'); END;
CREATE TRIGGER drop_disposals_retained BEFORE DELETE ON drop_disposals BEGIN SELECT RAISE(ABORT,'retained disposal denial'); END;
CREATE TABLE drop_effects (
 attempt_operation_id TEXT NOT NULL REFERENCES operations(id), step TEXT NOT NULL CHECK(step IN ('content','absence','drain')),
 canonical BLOB NOT NULL CHECK(length(canonical)<=8192), digest TEXT NOT NULL, PRIMARY KEY(attempt_operation_id,step));
CREATE TRIGGER drop_effects_immutable BEFORE UPDATE ON drop_effects BEGIN SELECT RAISE(ABORT,'immutable drop effect'); END;
CREATE TRIGGER drop_effects_retained BEFORE DELETE ON drop_effects BEGIN SELECT RAISE(ABORT,'retained drop effect'); END;
CREATE TABLE drop_upload_files (
 upload_id TEXT PRIMARY KEY REFERENCES drop_uploads(id),
 canonical BLOB NOT NULL CHECK(length(canonical)<=8192), digest TEXT NOT NULL);
CREATE TRIGGER drop_upload_files_immutable BEFORE UPDATE ON drop_upload_files BEGIN SELECT RAISE(ABORT,'immutable upload file identity'); END;
CREATE TRIGGER drop_upload_files_retained BEFORE DELETE ON drop_upload_files BEGIN SELECT RAISE(ABORT,'retained upload file identity'); END;
CREATE TABLE drop_chunks (
 upload_id TEXT NOT NULL REFERENCES drop_uploads(id), offset INTEGER NOT NULL CHECK(offset>=0),
 length INTEGER NOT NULL CHECK(length BETWEEN 1 AND 1048576), digest TEXT NOT NULL,
 committed INTEGER NOT NULL DEFAULT 0 CHECK(committed IN (0,1)),
 PRIMARY KEY(upload_id,offset));
CREATE TRIGGER drop_chunks_identity BEFORE UPDATE OF upload_id,offset,length,digest ON drop_chunks BEGIN SELECT RAISE(ABORT,'immutable chunk intent'); END;
CREATE TRIGGER drop_chunks_monotone BEFORE UPDATE ON drop_chunks WHEN OLD.committed<>0 OR NEW.committed<>1 BEGIN SELECT RAISE(ABORT,'terminal chunk intent'); END;
CREATE TRIGGER drop_chunks_retained BEFORE DELETE ON drop_chunks BEGIN SELECT RAISE(ABORT,'retained chunk intent'); END;
CREATE TABLE drop_seals (
 upload_id TEXT PRIMARY KEY REFERENCES drop_uploads(id),
 canonical BLOB NOT NULL CHECK(length(canonical)<=4194304), digest TEXT NOT NULL);
CREATE TRIGGER drop_seals_immutable BEFORE UPDATE ON drop_seals BEGIN SELECT RAISE(ABORT,'immutable prepared seal'); END;
CREATE TRIGGER drop_seals_retained BEFORE DELETE ON drop_seals BEGIN SELECT RAISE(ABORT,'retained prepared seal'); END;
`
