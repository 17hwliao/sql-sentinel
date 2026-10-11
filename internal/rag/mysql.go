package rag

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"sqlsentinel/internal/identity"
	"time"
)

// MySQLVectorStore is a bounded exact-cosine index. It favors recoverable
// deployment over an implicit external vector service; 10000 chunks per scope.
type MySQLVectorStore struct {
	DB  *sql.DB
	Dim int
}

func validVector(vector []float64, dim int) bool {
	if len(vector) != dim {
		return false
	}
	for _, v := range vector {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}
func (s *MySQLVectorStore) Upsert(ctx context.Context, chunks []Chunk, vectors [][]float64) error {
	if len(chunks) == 0 || len(chunks) > 128 || len(chunks) != len(vectors) {
		return ErrInvalidInput
	}
	first := chunks[0]
	if first.Scope.validate() != nil || len(first.DocumentID) > 128 || len(first.Version) > 128 {
		return ErrInvalidInput
	}
	for i, c := range chunks {
		if c.Scope != first.Scope || c.DocumentID != first.DocumentID || c.Version != first.Version || c.DocumentSHA256 != first.DocumentSHA256 || c.ID == "" || !validVector(vectors[i], s.Dim) {
			return ErrInvalidInput
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO rag_scopes(tenant_id,project_id) VALUES (?,?)", first.Scope.TenantID, first.Scope.ProjectID); err != nil {
		return err
	}
	var tenant string
	if err = tx.QueryRowContext(ctx, "SELECT tenant_id FROM rag_scopes WHERE tenant_id=? AND project_id=? FOR UPDATE", first.Scope.TenantID, first.Scope.ProjectID).Scan(&tenant); err != nil {
		return err
	}
	var total int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM rag_chunks WHERE tenant_id=? AND project_id=? AND document_id<>?", first.Scope.TenantID, first.Scope.ProjectID, first.DocumentID).Scan(&total); err != nil {
		return err
	}
	if total+len(chunks) > 10000 {
		return errors.New("rag_scope_capacity")
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM rag_chunks WHERE tenant_id=? AND project_id=? AND document_id=?", first.Scope.TenantID, first.Scope.ProjectID, first.DocumentID); err != nil {
		return err
	}
	for i, c := range chunks {
		raw, e := json.Marshal(c)
		if e != nil {
			return e
		}
		vec, _ := json.Marshal(vectors[i])
		if _, err = tx.ExecContext(ctx, "INSERT INTO rag_chunks(chunk_id,tenant_id,project_id,document_id,chunk,vector) VALUES (?,?,?,?,?,?)", c.ID, c.Scope.TenantID, c.Scope.ProjectID, c.DocumentID, raw, vec); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO rag_documents(tenant_id,project_id,document_id,document_sha256,version) VALUES (?,?,?,?,?) ON DUPLICATE KEY UPDATE document_sha256=VALUES(document_sha256),version=VALUES(version)", first.Scope.TenantID, first.Scope.ProjectID, first.DocumentID, first.DocumentSHA256, first.Version); err != nil {
		return err
	}
	if err = ragAudit(ctx, tx, first.Scope, "rag.document.upserted", first.DocumentID, first.DocumentSHA256); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *MySQLVectorStore) Search(ctx context.Context, scope Scope, query []float64, options SearchOptions) ([]SearchHit, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	if !validVector(query, s.Dim) {
		return nil, ErrInvalidInput
	}
	if options.TopK <= 0 {
		options.TopK = DefaultTopK
	}
	if options.TopK > 20 {
		return nil, ErrInvalidInput
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT chunk,vector FROM rag_chunks WHERE tenant_id=? AND project_id=? ORDER BY chunk_id LIMIT 10001", scope.TenantID, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := []SearchHit{}
	count := 0
	for rows.Next() {
		count++
		if count > 10000 {
			return nil, errors.New("rag_scope_capacity")
		}
		var raw, vectorRaw []byte
		if err = rows.Scan(&raw, &vectorRaw); err != nil {
			return nil, err
		}
		var c Chunk
		var v []float64
		if len(raw) > 32<<10 || len(vectorRaw) > 256<<10 || json.Unmarshal(raw, &c) != nil || json.Unmarshal(vectorRaw, &v) != nil || !validVector(v, s.Dim) {
			return nil, ErrUnavailable
		}
		if c.Scope != scope || isExpired(c.ExpiresAt, options.Now) || !c.Untrusted {
			continue
		}
		score := cosine(query, v)
		if score < options.MinScore {
			continue
		}
		hits = append(hits, SearchHit{Chunk: c, Score: score})
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].Score == hits[j].Score {
				return hits[i].Chunk.ID < hits[j].Chunk.ID
			}
			return hits[i].Score > hits[j].Score
		})
		if len(hits) > options.TopK {
			hits = hits[:options.TopK]
		}
	}
	return hits, rows.Err()
}
func (s *MySQLVectorStore) DeleteDocument(ctx context.Context, scope Scope, id string) error {
	if scope.validate() != nil || id == "" {
		return ErrInvalidInput
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tenant string
	if err = tx.QueryRowContext(ctx, "SELECT tenant_id FROM rag_scopes WHERE tenant_id=? AND project_id=? FOR UPDATE", scope.TenantID, scope.ProjectID).Scan(&tenant); err == sql.ErrNoRows {
		return ErrDocumentNotFound
	} else if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM rag_documents WHERE tenant_id=? AND project_id=? AND document_id=?", scope.TenantID, scope.ProjectID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrDocumentNotFound
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM rag_chunks WHERE tenant_id=? AND project_id=? AND document_id=?", scope.TenantID, scope.ProjectID, id); err != nil {
		return err
	}
	if err = ragAudit(ctx, tx, scope, "rag.document.deleted", id, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func ragAudit(ctx context.Context, tx *sql.Tx, scope Scope, action, id, sha string) error {
	p := identity.From(ctx)
	if p.Subject == "" {
		p.Subject = "adapter"
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO sql_sentinel_audit(tenant_id,project_id,actor,action,resource_id,metadata) VALUES (?,?,?,?,?,JSON_OBJECT('document_sha256',?))", scope.TenantID, scope.ProjectID, p.Subject, action, id, sha)
	return err
}
