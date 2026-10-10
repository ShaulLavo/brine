package data

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// SchemaArtifact is the non-executable evidence sent with a release declaration.
type SchemaArtifact struct {
	Definition SchemaDefinition `json:"definition"`
	Digest     string           `json:"digest"`
}

func (a SchemaInitializer) Artifact() (SchemaArtifact, []byte, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return SchemaArtifact{}, nil, err
	}
	sum := sha256.Sum256(raw)
	return SchemaArtifact{Definition: a.Definition, Digest: "sha256:" + hex.EncodeToString(sum[:])}, raw, nil
}

var schemaNumber = regexp.MustCompile(`^(?:0[xX][0-9a-fA-F](?:_?[0-9a-fA-F])*|(?:[0-9](?:_?[0-9])*(?:\.[0-9]*(?:_?[0-9])*)?|\.[0-9](?:_?[0-9])*)(?:[eE][+-]?[0-9](?:_?[0-9])*)?)`)

type schemaToken struct {
	text string
	line int
}
type schemaEntry struct {
	statement string
	line      int
}

// CompileSchemaFile shares the host initializer compiler and catalog observer.
func CompileSchemaFile(ctx context.Context, database DatabaseName, marker string, raw []byte) (SchemaInitializer, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entries, err := schemaEntries(string(raw), true)
	if err != nil {
		return SchemaInitializer{}, err
	}
	statements := make([]string, len(entries))
	for i, entry := range entries {
		statements[i] = entry.statement
	}
	hash, err := compileInitializer(ctx, entries)
	if err != nil {
		return SchemaInitializer{}, err
	}
	if marker == "" {
		marker = "sha256:" + hash[:12]
	}
	a := SchemaInitializer{Definition: SchemaDefinition{Database: database, Marker: marker, CatalogSHA256: hash}, Statements: statements}
	if err := a.Validate(ctx); err != nil {
		return SchemaInitializer{}, err
	}
	return a, nil
}

func compileInitializer(ctx context.Context, entries []schemaEntry) (string, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, entry.statement); err != nil {
			return "", schemaLineError(entry.line)
		}
	}
	hash, err := initializerCatalog(ctx, tx)
	if err != nil || hash == EmptyCatalogSHA256 || finishInitializer(ctx, tx, "compiler", hash) != nil {
		return "", schemaLineError(entries[len(entries)-1].line)
	}
	return hash, nil
}

type SchemaError struct{ Line int }

func (e *SchemaError) Error() string {
	return fmt.Sprintf("schema line %d: unsupported or invalid DDL", e.Line)
}

func schemaLineError(line int) error { return &SchemaError{Line: line} }

func schemaEntries(raw string, terminated bool) ([]schemaEntry, error) {
	if len(raw) > maxCatalogBytes || strings.ContainsRune(raw, 0) || !utf8.ValidString(raw) {
		return nil, schemaLineError(1)
	}
	entries := []schemaEntry{}
	tokens := []schemaToken{}
	line := 1
	finish := func() error {
		if len(tokens) == 0 {
			return schemaLineError(line)
		}
		words := make([]string, len(tokens))
		for i, t := range tokens {
			words[i] = t.text
		}
		upper := strings.ToUpper(strings.Join(words, " "))
		if !(strings.HasPrefix(upper, "CREATE TABLE ") || strings.HasPrefix(upper, "CREATE INDEX ") || strings.HasPrefix(upper, "CREATE UNIQUE INDEX ") || strings.HasPrefix(upper, "CREATE VIEW ")) {
			return schemaLineError(tokens[0].line)
		}
		if strings.HasPrefix(upper, "CREATE TABLE ") {
			for _, word := range words {
				if strings.EqualFold(word, "SELECT") {
					return schemaLineError(tokens[0].line)
				}
			}
		}
		var b strings.Builder
		for i, t := range tokens {
			if i > 0 && t.text != ")" && t.text != "," && t.text != "." && t.text != "(" && tokens[i-1].text != "(" && tokens[i-1].text != "." && !(strings.EqualFold(tokens[i-1].text, "X") && strings.HasPrefix(t.text, "'")) {
				b.WriteByte(' ')
			}
			b.WriteString(t.text)
		}
		entries = append(entries, schemaEntry{b.String(), tokens[0].line})
		tokens = nil
		if len(entries) > 128 {
			return schemaLineError(line)
		}
		return nil
	}
	for i := 0; i < len(raw); {
		c := raw[i]
		if strings.ContainsRune(" \t\r\n\f\v", rune(c)) {
			if c == '\n' {
				line++
			}
			i++
			continue
		}
		if i+1 < len(raw) && raw[i:i+2] == "--" {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(raw) && raw[i:i+2] == "/*" {
			startLine := line
			i += 2
			for i+1 < len(raw) && raw[i:i+2] != "*/" {
				if raw[i] == '\n' {
					line++
				}
				i++
			}
			if i+1 >= len(raw) {
				return nil, schemaLineError(startLine)
			}
			i += 2
			continue
		}
		if c == ';' {
			if !terminated {
				return nil, schemaLineError(line)
			}
			if err := finish(); err != nil {
				return nil, err
			}
			i++
			continue
		}
		start, startLine := i, line
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			end := c
			if c == '[' {
				end = ']'
			}
			i++
			closed := false
			for i < len(raw) {
				if raw[i] == '\n' {
					line++
				}
				if raw[i] == end {
					i++
					if c != '[' && i < len(raw) && raw[i] == end {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, schemaLineError(startLine)
			}
		} else if (c >= '0' && c <= '9') || (c == '.' && i+1 < len(raw) && raw[i+1] >= '0' && raw[i+1] <= '9') {
			i += len(schemaNumber.FindString(raw[i:]))
		} else if strings.ContainsRune("(),.+-*/%=<>!|&~", rune(c)) {
			i++
			if i < len(raw) && (raw[start:i+1] == "||" || raw[start:i+1] == "<=" || raw[start:i+1] == ">=" || raw[start:i+1] == "<>" || raw[start:i+1] == "!=" || raw[start:i+1] == "==" || raw[start:i+1] == "<<" || raw[start:i+1] == ">>" || raw[start:i+1] == "->") {
				i++
			}
			if raw[start:i] == "->" && i < len(raw) && raw[i] == '>' {
				i++
			}
		} else {
			for i < len(raw) && !strings.ContainsRune(" \t\r\n\f\v", rune(raw[i])) && !strings.ContainsRune(";(),.+-*/%=<>!|&~'\"`[", rune(raw[i])) {
				i++
			}
		}
		tokens = append(tokens, schemaToken{raw[start:i], startLine})
	}
	if len(tokens) > 0 {
		if terminated {
			return nil, schemaLineError(tokens[0].line)
		}
		if err := finish(); err != nil {
			return nil, err
		}
	}
	if len(entries) == 0 {
		return nil, schemaLineError(1)
	}
	return entries, nil
}

func initializerEntries(statements []string) ([]schemaEntry, error) {
	if len(statements) == 0 || len(statements) > 128 {
		return nil, ErrInvalid
	}
	entries := make([]schemaEntry, 0, len(statements))
	total := 0
	for _, statement := range statements {
		total += len(statement)
		if total > maxCatalogBytes {
			return nil, ErrInvalid
		}
		parsed, err := schemaEntries(statement, false)
		if err != nil || len(parsed) != 1 {
			return nil, ErrInvalid
		}
		entries = append(entries, parsed[0])
	}
	return entries, nil
}
