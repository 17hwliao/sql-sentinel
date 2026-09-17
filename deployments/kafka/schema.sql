-- The application owns the identical DDL in internal/kafka.MySQLStore.Schema.
-- Keep this file for operators and fresh control-plane bootstrapping.
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
