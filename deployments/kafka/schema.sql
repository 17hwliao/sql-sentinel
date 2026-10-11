CREATE TABLE IF NOT EXISTS sql_sentinel_prior_outbox (
 job_id CHAR(64) NOT NULL PRIMARY KEY,document JSON NOT NULL,attempts INT NOT NULL DEFAULT 0,
 available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),published_at DATETIME(6) NULL,
 expires_at DATETIME(6) NOT NULL,KEY ix_prior_ready(published_at,available_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_receive_guard (
 guard_id INT NOT NULL PRIMARY KEY
) ENGINE=InnoDB;
INSERT IGNORE INTO sql_sentinel_receive_guard(guard_id) VALUES(1);
CREATE TABLE IF NOT EXISTS sql_sentinel_job_options (
 job_id CHAR(64) NOT NULL PRIMARY KEY, options JSON NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_approvals (
 job_id CHAR(64) NOT NULL PRIMARY KEY, report_sha256 CHAR(64) NOT NULL,
 candidate_sha256 CHAR(64) NOT NULL, sql_sha256 CHAR(64) NOT NULL,
 decision VARCHAR(16) NOT NULL, actor VARCHAR(64) NOT NULL, reason VARCHAR(512) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_audit (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 tenant_id VARCHAR(64) NOT NULL,project_id VARCHAR(64) NOT NULL,actor VARCHAR(64) NOT NULL,
 action VARCHAR(64) NOT NULL,resource_id VARCHAR(128) NOT NULL,metadata JSON NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 KEY idx_audit_scope(tenant_id,project_id,id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_sessions (
 token_id CHAR(32) NOT NULL PRIMARY KEY,subject_id VARCHAR(64) NOT NULL,
 tenant_id VARCHAR(64) NOT NULL,project_id VARCHAR(64) NOT NULL,role_name VARCHAR(32) NOT NULL,
 expires_at DATETIME(6) NOT NULL,revoked_at DATETIME(6) NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS rag_scopes (
 tenant_id VARCHAR(64) NOT NULL,project_id VARCHAR(64) NOT NULL,
 PRIMARY KEY(tenant_id,project_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS rag_documents (
 tenant_id VARCHAR(64) NOT NULL,project_id VARCHAR(64) NOT NULL,document_id VARCHAR(128) NOT NULL,
 document_sha256 CHAR(64) NOT NULL,version VARCHAR(128) NOT NULL,
 PRIMARY KEY(tenant_id,project_id,document_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS rag_chunks (
 chunk_id CHAR(32) NOT NULL PRIMARY KEY,tenant_id VARCHAR(64) NOT NULL,project_id VARCHAR(64) NOT NULL,
 document_id VARCHAR(128) NOT NULL,chunk JSON NOT NULL,vector JSON NOT NULL,
 KEY idx_rag_scope(tenant_id,project_id,document_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_sources (
 job_id CHAR(64) NOT NULL PRIMARY KEY, source JSON NOT NULL,
 comment_published_at DATETIME(6) NULL, comment_state VARCHAR(32) NOT NULL DEFAULT '',
 KEY idx_comment_pending(comment_published_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_poison (
 topic VARCHAR(249) NOT NULL, partition_id INT NOT NULL, offset_id BIGINT NOT NULL,
 payload_sha256 CHAR(64) NOT NULL, reason VARCHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(topic,partition_id,offset_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_reports (
 job_id CHAR(64) NOT NULL PRIMARY KEY, report JSON NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_jobs (
  job_id CHAR(64) NOT NULL PRIMARY KEY,
  delivery_id VARCHAR(64) NOT NULL UNIQUE,
  pr_number BIGINT NOT NULL,
  sql_text MEDIUMBLOB NOT NULL,
  candidate_spec MEDIUMBLOB NOT NULL,
  sql_sha256 CHAR(64) NOT NULL,
  candidate_spec_sha256 CHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL,
  attempts INT NOT NULL DEFAULT 0,
  next_attempt_at DATETIME(6) NOT NULL,
  lease_owner VARCHAR(128) NOT NULL DEFAULT '',
  lease_until DATETIME(6) NULL,
  last_error VARCHAR(1024) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  completed_at DATETIME(6) NULL,
  KEY idx_jobs_claim (status, next_attempt_at),
  KEY idx_jobs_lease (status, lease_until)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_job_history (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  job_id CHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL,
  reason VARCHAR(1024) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL,
  KEY idx_history_job (job_id, id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_outbox (
  event_id CHAR(64) NOT NULL PRIMARY KEY,
  job_id CHAR(64) NOT NULL,
  topic VARCHAR(249) NOT NULL,
  payload JSON NOT NULL,
  attempts INT NOT NULL DEFAULT 0,
  available_at DATETIME(6) NOT NULL,
  published_at DATETIME(6) NULL,
  last_error VARCHAR(1024) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL,
  KEY idx_outbox_ready (published_at, available_at)
) ENGINE=InnoDB;
