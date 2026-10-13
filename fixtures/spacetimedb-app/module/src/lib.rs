use spacetimedb::{Identity, ReducerContext, ScheduleAt, Table, Timestamp};

const MAX_ROWS: u64 = 10_000;

#[cfg(not(feature = "default-column"))]
#[cfg_attr(not(feature = "access-change"), spacetimedb::table(accessor = recovery_left, public))]
#[cfg_attr(feature = "access-change", spacetimedb::table(accessor = recovery_left))]
pub struct RecoveryLeft {
    #[primary_key]
    pub sequence: u64,
    pub marker: String,
    pub commit_time: Timestamp,
    pub scheduled: bool,
}

#[cfg(feature = "default-column")]
#[spacetimedb::table(accessor = recovery_left, public)]
pub struct RecoveryLeft {
    #[primary_key]
    pub sequence: u64,
    pub marker: String,
    pub commit_time: Timestamp,
    pub scheduled: bool,
    #[default(7)]
    pub rating: u8,
}

#[cfg(all(not(feature = "destructive"), not(feature = "default-column")))]
#[spacetimedb::table(accessor = recovery_right, public)]
pub struct RecoveryRight {
    #[primary_key]
    pub sequence: u64,
    pub marker: String,
    pub commit_time: Timestamp,
    pub scheduled: bool,
}

#[cfg(feature = "default-column")]
#[spacetimedb::table(accessor = recovery_right, public)]
pub struct RecoveryRight {
    #[primary_key]
    pub sequence: u64,
    pub marker: String,
    pub commit_time: Timestamp,
    pub scheduled: bool,
    #[default(7)]
    pub rating: u8,
}

#[spacetimedb::table(accessor = schema_marker, public)]
pub struct SchemaMarker {
    #[primary_key]
    pub id: u8,
    pub revision: String,
}

#[spacetimedb::table(accessor = publisher)]
pub struct Publisher {
    #[primary_key]
    pub id: u8,
    pub identity: Identity,
}

#[spacetimedb::table(accessor = writer)]
pub struct Writer {
    #[primary_key]
    pub identity: Identity,
}

#[spacetimedb::table(accessor = sequence_state)]
pub struct SequenceState {
    #[primary_key]
    pub id: u8,
    pub sequence: u64,
}

#[spacetimedb::table(accessor = scheduled_write, scheduled(tick))]
pub struct ScheduledWrite {
    #[primary_key]
    #[auto_inc]
    pub scheduled_id: u64,
    pub scheduled_at: ScheduleAt,
    pub remaining: u8,
}

#[spacetimedb::reducer(init)]
pub fn init(ctx: &ReducerContext) {
    ctx.db.publisher().insert(Publisher {
        id: 1,
        identity: ctx.sender(),
    });
    ctx.db.schema_marker().insert(SchemaMarker {
        id: 1,
        revision: "brine-recovery-v1".into(),
    });
    ctx.db
        .sequence_state()
        .insert(SequenceState { id: 1, sequence: 0 });
}

fn require_owner(ctx: &ReducerContext) -> Result<(), String> {
    let owner = ctx.db.publisher().id().find(1).ok_or("missing publisher")?;
    if ctx.sender() != owner.identity {
        return Err("publisher identity required".into());
    }
    Ok(())
}

#[spacetimedb::reducer]
pub fn authorize_writer(ctx: &ReducerContext, identity: Identity) -> Result<(), String> {
    require_owner(ctx)?;
    if ctx.db.writer().count() >= 4 {
        return Err("writer limit reached".into());
    }
    ctx.db
        .writer()
        .try_insert(Writer { identity })
        .map_err(|_| String::from("writer already exists"))?;
    Ok(())
}

fn write(ctx: &ReducerContext, sequence: u64, scheduled: bool) -> Result<(), String> {
    let mut state = ctx
        .db
        .sequence_state()
        .id()
        .find(1)
        .ok_or("missing sequence state")?;
    if sequence != state.sequence + 1 || sequence > MAX_ROWS {
        return Err(sequence_error(sequence, state.sequence).into());
    }
    let marker = format!("brine-p09-{sequence:08}");
    ctx.db.recovery_left().insert(RecoveryLeft {
        sequence,
        marker: marker.clone(),
        commit_time: ctx.timestamp,
        scheduled,
        #[cfg(feature = "default-column")]
        rating: 7,
    });
    #[cfg(not(feature = "destructive"))]
    ctx.db.recovery_right().insert(RecoveryRight {
        sequence,
        marker,
        commit_time: ctx.timestamp,
        scheduled,
        #[cfg(feature = "default-column")]
        rating: 7,
    });
    state.sequence = sequence;
    ctx.db.sequence_state().id().update(state);
    Ok(())
}

fn sequence_error(sequence: u64, committed: u64) -> &'static str {
    if cfg!(feature = "logic-only") && sequence <= committed {
        return "sequence already committed";
    }
    "expected next sequence within row limit"
}

#[spacetimedb::reducer]
pub fn append(ctx: &ReducerContext, sequence: u64) -> Result<(), String> {
    if ctx.db.writer().identity().find(ctx.sender()).is_none() {
        return Err("authorized writer required".into());
    }
    if ctx.db.scheduled_write().count() != 0 {
        return Err("scheduled writer active".into());
    }
    write(ctx, sequence, false)
}

#[spacetimedb::reducer]
pub fn schedule_writes(ctx: &ReducerContext, count: u8) -> Result<(), String> {
    require_owner(ctx)?;
    if count == 0 || count > 20 || ctx.db.scheduled_write().count() != 0 {
        return Err("one schedule of 1..20 writes required".into());
    }
    let state = ctx
        .db
        .sequence_state()
        .id()
        .find(1)
        .ok_or("missing sequence state")?;
    if state.sequence + u64::from(count) > MAX_ROWS {
        return Err("schedule exceeds row limit".into());
    }
    ctx.db.scheduled_write().insert(ScheduledWrite {
        scheduled_id: 0,
        scheduled_at: std::time::Duration::from_secs(1).into(),
        remaining: count,
    });
    Ok(())
}

#[spacetimedb::reducer]
pub fn tick(ctx: &ReducerContext, arg: ScheduledWrite) -> Result<(), String> {
    if ctx.sender() != ctx.database_identity() {
        return Err("scheduler identity required".into());
    }
    let mut row = ctx
        .db
        .scheduled_write()
        .scheduled_id()
        .find(arg.scheduled_id)
        .ok_or("schedule absent")?;
    let state = ctx
        .db
        .sequence_state()
        .id()
        .find(1)
        .ok_or("missing sequence state")?;
    write(ctx, state.sequence + 1, true)?;
    if row.remaining == 1 {
        ctx.db
            .scheduled_write()
            .scheduled_id()
            .delete(row.scheduled_id);
        return Ok(());
    }
    row.remaining -= 1;
    ctx.db.scheduled_write().scheduled_id().update(row);
    Ok(())
}

#[cfg(feature = "additive")]
#[spacetimedb::table(accessor = fixture_note, public)]
pub struct FixtureNote {
    #[primary_key]
    pub id: u8,
    pub note: String,
}

#[cfg(feature = "observer-hooks")]
#[spacetimedb::reducer(client_connected)]
pub fn observer_negative(ctx: &ReducerContext) {
    ctx.db.schema_marker().id().update(SchemaMarker {
        id: 1,
        revision: "observer-ran-reducer".into(),
    });
}

const _: () = assert!(
    cfg!(feature = "logic-only") as u8
        + cfg!(feature = "additive") as u8
        + cfg!(feature = "destructive") as u8
        + cfg!(feature = "observer-hooks") as u8
        + cfg!(feature = "default-column") as u8
        + cfg!(feature = "access-change") as u8
        <= 1,
    "fixture revisions are mutually exclusive"
);
