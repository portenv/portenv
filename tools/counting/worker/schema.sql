-- SPDX-License-Identifier: Apache-2.0
--
-- The counting Worker's only table (PLAN.md 1.8, R-0010 point 1): one
-- counter per UTC day, field and value. Never a row per check, never a row
-- combining fields (except `cw`, install month x week active, the one
-- cross-tab the snapshot needs), never an IP address or a header. The counts
-- are anonymous aggregates and are kept; nothing request-level exists.
CREATE TABLE IF NOT EXISTS counts (
  day   TEXT    NOT NULL, -- yyyy-mm-dd, UTC
  field TEXT    NOT NULL, -- checks, v, os, arch, c, w, cw, m, moved, agent, boxes, servers
  value TEXT    NOT NULL,
  n     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, field, value)
) WITHOUT ROWID;
