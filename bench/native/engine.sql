-- Copyright (c) 2026 The liustack/sqlite Authors.
-- SPDX-License-Identifier: MIT
-- Use of this source code is governed by the MIT license in LICENSE at the
-- root of this repository.
--
-- The work inside the engine that c/engine.c and main.go time, each on one
-- connection to :memory:. A block starts with @@, a space and its name,
-- then the SQL that sets it up, the line @@run and the SQL that is timed,
-- the line @@check and a query of one text value that both sides must give
-- alike.

@@ insert
CREATE TABLE IF NOT EXISTS z(x);
@@run
DROP TABLE IF EXISTS b; CREATE TABLE b(a INTEGER, s TEXT, c REAL);
WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 1000000)
INSERT INTO b SELECT i, printf('k%08d-%x', (i*7919) % 1000003, i), i * 0.37 FROM r;
@@check
SELECT count(*) || ' ' || sum(a) || ' ' || sum(length(s)) FROM b;
@@ index
CREATE TABLE t(a INTEGER, s TEXT, c REAL);
WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 1000000)
INSERT INTO t SELECT i, printf('k%08d-%x', (i*7919) % 1000003, i), i * 0.37 FROM r;
@@run
DROP INDEX IF EXISTS ts; CREATE INDEX ts ON t(s);
@@check
SELECT s FROM t INDEXED BY ts ORDER BY s LIMIT 1 OFFSET 500000;
@@ sort
CREATE TABLE t(a INTEGER, s TEXT, c REAL);
WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 1000000)
INSERT INTO t SELECT i, printf('k%08d-%x', (i*7919) % 1000003, i), i * 0.37 FROM r;
@@run
SELECT sum(length(s)) FROM (SELECT s FROM t ORDER BY s LIMIT 1000000);
@@check
SELECT group_concat(s, ' ') FROM (SELECT s FROM t ORDER BY s LIMIT 3 OFFSET 700000);
@@ group
CREATE TABLE t(a INTEGER, s TEXT, c REAL);
WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 1000000)
INSERT INTO t SELECT i, printf('k%08d-%x', (i*7919) % 1000003, i), i * 0.37 FROM r;
@@run
SELECT count(*), sum(av) FROM (SELECT a % 1000 AS g, avg(c) AS av, count(*) FROM t GROUP BY g);
@@check
SELECT count(*) || ' ' || round(sum(av), 6) FROM (SELECT a % 1000 AS g, avg(c) AS av FROM t GROUP BY g);
@@ join
CREATE TABLE t(a INTEGER PRIMARY KEY, s TEXT, c REAL);
WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 1000000)
INSERT INTO t SELECT i, printf('k%08d-%x', (i*7919) % 1000003, i), i * 0.37 FROM r;
@@run
SELECT count(*), sum(y.c) FROM t x JOIN t y ON y.a = (x.a * 7) % 1000000 + 1 WHERE x.a <= 300000;
@@check
SELECT count(*) || ' ' || round(sum(y.c), 6) FROM t x JOIN t y ON y.a = (x.a * 7) % 1000000 + 1 WHERE x.a <= 300000;
@@ cte
CREATE TABLE IF NOT EXISTS z(x);
@@run
WITH RECURSIVE r(i, f) AS (SELECT 1, 1 UNION ALL SELECT i+1, (f * 31 + i) % 1000003 FROM r WHERE i < 2000000) SELECT sum(f) FROM r;
@@check
WITH RECURSIVE r(i, f) AS (SELECT 1, 1 UNION ALL SELECT i+1, (f * 31 + i) % 1000003 FROM r WHERE i < 2000000) SELECT sum(f) || '' FROM r;
@@ like
CREATE TABLE t(a INTEGER, s TEXT, c REAL);
WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 1000000)
INSERT INTO t SELECT i, printf('k%08d-%x', (i*7919) % 1000003, i), i * 0.37 FROM r;
@@run
SELECT count(*), sum(length(upper(substr(s, 2, 6)) || replace(s, 'k', 'q'))) FROM t WHERE s LIKE '%12%';
@@check
SELECT count(*) || ' ' || sum(length(upper(substr(s, 2, 6)) || replace(s, 'k', 'q'))) FROM t WHERE s LIKE '%12%';
@@ json
CREATE TABLE t(a INTEGER, s TEXT, c REAL);
WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 300000)
INSERT INTO t SELECT i, printf('k%08d-%x', (i*7919) % 1000003, i), i * 0.37 FROM r;
@@run
SELECT sum(json_extract(j, '$.a')), count(json_extract(j, '$.s')) FROM (SELECT json_object('a', a, 's', s, 'c', c, 'l', json_array(a, c)) AS j FROM t);
@@check
SELECT sum(json_extract(j, '$.a')) || ' ' || count(json_extract(j, '$.s')) FROM (SELECT json_object('a', a, 's', s, 'c', c, 'l', json_array(a, c)) AS j FROM t);
@@ math
CREATE TABLE t(a INTEGER, s TEXT, c REAL);
WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 1000000)
INSERT INTO t SELECT i, printf('k%08d-%x', (i*7919) % 1000003, i), i * 0.37 FROM r;
@@run
SELECT sum(sqrt(c) + sin(c) + log(c + 1) + pow(c, 0.3)) FROM t;
@@check
SELECT round(sum(sqrt(c) + sin(c) + log(c + 1) + pow(c, 0.3)), 3) || '' FROM t;
@@ fts5
CREATE VIRTUAL TABLE f USING fts5(body);
WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM r WHERE i < 200000)
INSERT INTO f SELECT printf('w%d w%d w%d alpha%d beta%d gamma%d', i % 97, i % 1013, i % 7919, i % 13, i % 101, i % 1009) FROM r;
@@run
SELECT (SELECT count(*) FROM f WHERE f MATCH 'w5') + (SELECT count(*) FROM f WHERE f MATCH 'alpha3 AND beta7') + (SELECT count(*) FROM f WHERE f MATCH 'gamma1*') + (SELECT count(*) FROM f WHERE f MATCH '"w11 w11"');
@@check
SELECT ((SELECT count(*) FROM f WHERE f MATCH 'w5') + (SELECT count(*) FROM f WHERE f MATCH 'alpha3 AND beta7') + (SELECT count(*) FROM f WHERE f MATCH 'gamma1*') + (SELECT count(*) FROM f WHERE f MATCH '"w11 w11"')) || '';
