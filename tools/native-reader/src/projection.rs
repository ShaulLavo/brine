use crate::protocol::{Deadline, Limits, Refusal, Result, TableEvidence, add};
use serde::Serialize;
use spacetimedb_datastore::execution_context::Workload;
use spacetimedb_engine::relational_db::RelationalDB;
use spacetimedb_lib::Identity;
use spacetimedb_sats::{AlgebraicType, ProductValue, bsatn};
use spacetimedb_schema::schema::TableSchema;
use spacetimedb_table::{read_column::ReadColumn, table::RowRef};
use std::io::Write;

pub const FORMAT: &str = "brine-confirmed-pairs-projection-v1";

#[derive(Debug, PartialEq, Serialize)]
pub struct Projection {
    pub format: &'static str,
    pub left: PairTable,
    pub right: PairTable,
    pub marker: MarkerTable,
    pub note: Option<NoteTable>,
    pub publisher: PublisherTable,
    pub sequence: SequenceTable,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct PairTable {
    pub table_id: u32,
    pub name: String,
    pub layout: &'static str,
    pub rows: Vec<PairRow>,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct PairRow {
    pub sequence: u64,
    pub marker: Box<str>,
    pub commit_time_micros: i64,
    pub scheduled: bool,
    pub rating: Option<u8>,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct MarkerTable {
    pub table_id: u32,
    pub name: String,
    pub rows: Vec<MarkerRow>,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct MarkerRow {
    pub id: u8,
    pub revision: Box<str>,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct NoteTable {
    pub table_id: u32,
    pub name: String,
    pub rows: Vec<NoteRow>,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct NoteRow {
    pub id: u8,
    pub note: Box<str>,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct PublisherTable {
    pub table_id: u32,
    pub name: String,
    pub rows: Vec<PublisherRow>,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct PublisherRow {
    pub id: u8,
    pub identity: String,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct SequenceTable {
    pub table_id: u32,
    pub name: String,
    pub rows: Vec<SequenceRow>,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct SequenceRow {
    pub id: u8,
    pub sequence: u64,
}

struct Budget<'a> {
    rows: u64,
    bytes: u64,
    output: u64,
    limits: &'a Limits,
    deadline: &'a Deadline,
}

impl Budget<'_> {
    fn charge(&mut self, row: RowRef<'_>) -> Result<()> {
        self.deadline.check()?;
        add(&mut self.rows, 1, self.limits.rows, Refusal::StateLimit)?;
        let size = bsatn::to_len(&row).map_err(|_| Refusal::Native)? as u64;
        if size > self.limits.row_bytes {
            return Err(Refusal::StateLimit);
        }
        add(
            &mut self.bytes,
            size,
            self.limits.state_bytes,
            Refusal::StateLimit,
        )
    }

    fn value(&mut self, value: &impl Serialize) -> Result<()> {
        let mut counter = Counter {
            bytes: 0,
            maximum: self.limits.output_bytes - self.output,
        };
        serde_json::to_writer(&mut counter, value).map_err(|_| Refusal::OutputLimit)?;
        self.output += counter.bytes;
        Ok(())
    }
}

struct Counter {
    bytes: u64,
    maximum: u64,
}

impl Write for Counter {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        self.bytes = self
            .bytes
            .checked_add(bytes.len() as u64)
            .filter(|size| *size <= self.maximum)
            .ok_or_else(|| std::io::Error::other("projection output limit"))?;
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

pub fn observe(
    db: &RelationalDB,
    summaries: &[TableEvidence],
    limits: &Limits,
    deadline: &Deadline,
) -> Result<Option<Projection>> {
    let tx = db.begin_tx(Workload::Internal);
    let schemas = db.get_all_tables(&tx).map_err(|_| Refusal::Native)?;
    let find = |name: &str| {
        schemas
            .iter()
            .find(|schema| schema.table_name.as_ref() == name)
    };
    let Some(left) = find("recovery_left") else {
        return Ok(None);
    };
    let Some(right) = find("recovery_right") else {
        return Ok(None);
    };
    let Some(marker) = find("schema_marker") else {
        return Ok(None);
    };
    let Some(publisher) = find("publisher") else {
        return Ok(None);
    };
    let Some(sequence) = find("sequence_state") else {
        return Ok(None);
    };
    let note = find("fixture_note");
    let Some(left_layout) = pair_layout(left) else {
        return Ok(None);
    };
    let Some(right_layout) = pair_layout(right) else {
        return Ok(None);
    };
    if !two_columns(marker, "revision", AlgebraicType::String)
        || note.is_some_and(|schema| !two_columns(schema, "note", AlgebraicType::String))
        || !two_columns(publisher, "identity", AlgebraicType::identity())
        || !two_columns(sequence, "sequence", AlgebraicType::U64)
    {
        return Ok(None);
    }
    let mut budget = Budget {
        rows: summaries.iter().map(|table| table.rows).sum(),
        bytes: summaries.iter().map(|table| table.bsatn_bytes).sum(),
        output: 0,
        limits,
        deadline,
    };
    let left_rows = db.iter(&tx, left.table_id).map_err(|_| Refusal::Native)?;
    let left = pair_table(left, left_layout, left_rows, &mut budget)?;
    let right_rows = db.iter(&tx, right.table_id).map_err(|_| Refusal::Native)?;
    let right = pair_table(right, right_layout, right_rows, &mut budget)?;
    let marker_rows = db.iter(&tx, marker.table_id).map_err(|_| Refusal::Native)?;
    let marker = marker_table(marker, marker_rows, &mut budget)?;
    let publisher_rows = db
        .iter(&tx, publisher.table_id)
        .map_err(|_| Refusal::Native)?;
    let publisher = publisher_table(publisher, publisher_rows, &mut budget)?;
    let sequence_rows = db
        .iter(&tx, sequence.table_id)
        .map_err(|_| Refusal::Native)?;
    let sequence = sequence_table(sequence, sequence_rows, &mut budget)?;
    let note = match note {
        Some(schema) => {
            let rows = db.iter(&tx, schema.table_id).map_err(|_| Refusal::Native)?;
            Some(note_table(schema, rows, &mut budget)?)
        }
        None => None,
    };
    let _ = db.release_tx(tx);
    Ok(Some(Projection {
        format: FORMAT,
        left,
        right,
        marker,
        note,
        publisher,
        sequence,
    }))
}

fn column(schema: &TableSchema, index: usize, name: &str, ty: AlgebraicType) -> bool {
    schema.columns.get(index).is_some_and(|column| {
        column.col_pos.idx() == index && &column.col_name[..] == name && column.col_type == ty
    })
}

fn pair_layout(schema: &TableSchema) -> Option<&'static str> {
    if !column(schema, 0, "sequence", AlgebraicType::U64)
        || !column(schema, 1, "marker", AlgebraicType::String)
        || !column(schema, 2, "commit_time", AlgebraicType::timestamp())
        || !column(schema, 3, "scheduled", AlgebraicType::Bool)
    {
        return None;
    }
    match schema.columns.len() {
        4 => Some("base-v1"),
        5 if column(schema, 4, "rating", AlgebraicType::U8) => Some("rating-v1"),
        _ => None,
    }
}

fn two_columns(schema: &TableSchema, name: &str, ty: AlgebraicType) -> bool {
    schema.columns.len() == 2
        && column(schema, 0, "id", AlgebraicType::U8)
        && column(schema, 1, name, ty)
}

fn read<T: ReadColumn>(row: RowRef<'_>, index: usize) -> Result<T> {
    row.read_col(index).map_err(|_| Refusal::Native)
}

fn pair_table<'a>(
    schema: &TableSchema,
    layout: &'static str,
    rows: impl Iterator<Item = RowRef<'a>>,
    budget: &mut Budget<'_>,
) -> Result<PairTable> {
    let mut output = Vec::new();
    for row in rows {
        budget.charge(row)?;
        let timestamp: ProductValue = read(row, 2)?;
        let micros = match timestamp.elements.as_ref() {
            [spacetimedb_sats::AlgebraicValue::I64(value)] => *value,
            _ => return Err(Refusal::Native),
        };
        let value = PairRow {
            sequence: read(row, 0)?,
            marker: read(row, 1)?,
            commit_time_micros: micros,
            scheduled: read(row, 3)?,
            rating: if layout == "rating-v1" {
                Some(read(row, 4)?)
            } else {
                None
            },
        };
        budget.value(&value)?;
        output.push(value);
    }
    output.sort_unstable_by_key(|row| row.sequence);
    Ok(PairTable {
        table_id: schema.table_id.0,
        name: schema.table_name.to_string(),
        layout,
        rows: output,
    })
}

fn marker_table<'a>(
    schema: &TableSchema,
    rows: impl Iterator<Item = RowRef<'a>>,
    budget: &mut Budget<'_>,
) -> Result<MarkerTable> {
    let mut output = Vec::new();
    for row in rows {
        budget.charge(row)?;
        let value = MarkerRow {
            id: read(row, 0)?,
            revision: read(row, 1)?,
        };
        budget.value(&value)?;
        output.push(value);
    }
    output.sort_unstable_by_key(|row| row.id);
    Ok(MarkerTable {
        table_id: schema.table_id.0,
        name: schema.table_name.to_string(),
        rows: output,
    })
}

fn note_table<'a>(
    schema: &TableSchema,
    rows: impl Iterator<Item = RowRef<'a>>,
    budget: &mut Budget<'_>,
) -> Result<NoteTable> {
    let mut output = Vec::new();
    for row in rows {
        budget.charge(row)?;
        let value = NoteRow {
            id: read(row, 0)?,
            note: read(row, 1)?,
        };
        budget.value(&value)?;
        output.push(value);
    }
    output.sort_unstable_by_key(|row| row.id);
    Ok(NoteTable {
        table_id: schema.table_id.0,
        name: schema.table_name.to_string(),
        rows: output,
    })
}

fn publisher_table<'a>(
    schema: &TableSchema,
    rows: impl Iterator<Item = RowRef<'a>>,
    budget: &mut Budget<'_>,
) -> Result<PublisherTable> {
    let mut output = Vec::new();
    for row in rows {
        budget.charge(row)?;
        let identity: ProductValue = read(row, 1)?;
        let identity = match identity.elements.as_ref() {
            [spacetimedb_sats::AlgebraicValue::U256(value)] => {
                Identity::from_u256(**value).to_string()
            }
            _ => return Err(Refusal::Native),
        };
        let value = PublisherRow {
            id: read(row, 0)?,
            identity,
        };
        budget.value(&value)?;
        output.push(value);
    }
    output.sort_unstable_by_key(|row| row.id);
    Ok(PublisherTable {
        table_id: schema.table_id.0,
        name: schema.table_name.to_string(),
        rows: output,
    })
}

fn sequence_table<'a>(
    schema: &TableSchema,
    rows: impl Iterator<Item = RowRef<'a>>,
    budget: &mut Budget<'_>,
) -> Result<SequenceTable> {
    let mut output = Vec::new();
    for row in rows {
        budget.charge(row)?;
        let value = SequenceRow {
            id: read(row, 0)?,
            sequence: read(row, 1)?,
        };
        budget.value(&value)?;
        output.push(value);
    }
    output.sort_unstable_by_key(|row| row.id);
    Ok(SequenceTable {
        table_id: schema.table_id.0,
        name: schema.table_name.to_string(),
        rows: output,
    })
}
