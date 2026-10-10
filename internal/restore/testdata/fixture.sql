CREATE TABLE fixture_commits(sequence INTEGER PRIMARY KEY, marker TEXT NOT NULL, committed_at TEXT NOT NULL);
CREATE TABLE brine_schema_marker(id INTEGER PRIMARY KEY CHECK(id=1), marker TEXT NOT NULL, catalog_sha256 TEXT NOT NULL);
