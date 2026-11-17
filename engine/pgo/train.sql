-- Copyright (c) 2026 The liustack/sqlite Authors.
-- SPDX-License-Identifier: MIT
-- Use of this source code is governed by the MIT license in LICENSE at the
-- root of this repository.
--
-- What the profile of the engine is trained on (engine/pgo/README.md): the
-- work of a program that uses the driver, each kind of it at least a little.
-- train.c runs it. A line that starts with @@ starts a block, and the SQL up
-- to the next such line belongs to it:
--
--   @@ NAME          a new section on a new database file, or with the word
--                    memory after the name on :memory:. Its SQL runs once.
--   @@run            each statement once
--   @@bind N         the statements prepared once and run N times in turn,
--                    each a commit of its own. A constraint may refuse one.
--   @@merge N K      the same as group commit runs them: K turns in one
--                    transaction, each statement in a SAVEPOINT of its own
--   @@prepare N      each statement prepared, run and finalized, N times
--   @@open N         N times a new connection that runs the statements
--   @@other          each statement once, on a second connection
--
-- The parameters of a statement get their values by name (bindAll of
-- train.c): :i the turn, :r and :q numbers that jump, :s a short text, :t a
-- line of words, :f a float, :b a blob, :n NULL. A statement of a @@bind or
-- @@merge block cannot use a table that an earlier one of the block makes.
--
-- None of this is taken from the benchmarks (bench/): they measure what the
-- profile does to work it was not trained on.

@@ app
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA foreign_keys=ON;
CREATE TABLE account(
  id INTEGER PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  tier INTEGER NOT NULL DEFAULT 0 CHECK (tier BETWEEN 0 AND 9),
  balance REAL NOT NULL DEFAULT 0,
  token BLOB,
  created INTEGER NOT NULL,
  note TEXT
);
CREATE TABLE purchase(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id INTEGER NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  state TEXT NOT NULL DEFAULT 'new',
  amount REAL NOT NULL,
  placed INTEGER NOT NULL
);
CREATE INDEX purchase_account ON purchase(account_id, placed DESC);
CREATE INDEX purchase_open ON purchase(placed) WHERE state = 'new';
CREATE TABLE line(
  purchase_id INTEGER NOT NULL REFERENCES purchase(id) ON DELETE CASCADE,
  sku TEXT NOT NULL,
  qty INTEGER NOT NULL,
  price REAL NOT NULL,
  PRIMARY KEY (purchase_id, sku)
) WITHOUT ROWID;
CREATE TABLE audit(at INTEGER, what TEXT, ref INTEGER);
CREATE TRIGGER purchase_audit AFTER INSERT ON purchase BEGIN
  INSERT INTO audit VALUES (new.placed, 'purchase', new.id);
  UPDATE account SET balance = balance - new.amount WHERE id = new.account_id;
END;
CREATE TRIGGER account_tier AFTER UPDATE OF balance ON account WHEN new.balance < -1000 AND old.tier < 9 BEGIN
  UPDATE account SET tier = tier + 1 WHERE id = new.id;
END;
CREATE VIEW account_spend AS
  SELECT a.id, a.name, count(p.id) AS n, coalesce(sum(p.amount), 0) AS spent
  FROM account a LEFT JOIN purchase p ON p.account_id = a.id GROUP BY a.id;
@@bind 20000
INSERT INTO account(id, email, name, tier, balance, token, created, note)
  VALUES (:i, :s || '@example.test', :t, :r % 10, :f, :b, 1700000000 + :i * 37, CASE WHEN :q % 3 = 0 THEN NULL ELSE :t END);
@@merge 30000 16
INSERT INTO purchase(account_id, state, amount, placed)
  VALUES (1 + :r % 20000, CASE :q % 4 WHEN 0 THEN 'new' WHEN 1 THEN 'paid' WHEN 2 THEN 'sent' ELSE 'done' END, (:q % 50000) / 100.0, 1700000000 + :i * 11);
INSERT INTO line(purchase_id, sku, qty, price)
  VALUES (last_insert_rowid(), 'sku-' || (:r % 997), 1 + :q % 5, (:r % 9000) / 100.0);
INSERT INTO line(purchase_id, sku, qty, price)
  VALUES (last_insert_rowid(), 'sku-' || (:q % 997), 1 + :r % 3, (:q % 9000) / 100.0)
  ON CONFLICT(purchase_id, sku) DO UPDATE SET qty = qty + excluded.qty;
@@bind 30000
SELECT id, email, name, tier, balance, token, created, note FROM account WHERE id = 1 + :r % 20000;
SELECT id, name FROM account WHERE email = :s || '@example.test';
SELECT id, state, amount, placed FROM purchase WHERE account_id = 1 + :q % 20000 ORDER BY placed DESC LIMIT 10;
@@bind 5000
SELECT p.id, p.amount, a.name, l.sku, l.qty * l.price FROM purchase p
  JOIN account a ON a.id = p.account_id JOIN line l ON l.purchase_id = p.id WHERE p.id = 1 + :r % 30000;
SELECT count(*), sum(amount) FROM purchase WHERE account_id = 1 + :r % 20000 AND state IN ('paid', 'sent');
SELECT id, placed FROM purchase WHERE state = 'new' AND placed > 1700000000 + (:r % 30000) * 11 ORDER BY placed LIMIT 5;
SELECT EXISTS (SELECT 1 FROM line WHERE purchase_id = 1 + :q % 30000 AND qty > 3);
SELECT name, tier FROM account WHERE id IN (1 + :r % 20000, 1 + :q % 20000, :i) ORDER BY name;
SELECT a.id, (SELECT max(placed) FROM purchase p WHERE p.account_id = a.id) FROM account a WHERE a.id BETWEEN 1 + :r % 20000 AND 5 + :r % 20000;
@@bind 200
SELECT name, n, spent FROM account_spend WHERE id = 1 + :r % 20000;
SELECT tier, count(*), avg(balance) FROM account GROUP BY tier ORDER BY tier;
@@merge 20000 8
UPDATE account SET balance = balance + :f / 100, note = :t WHERE id = 1 + :r % 20000;
UPDATE purchase SET state = 'paid' WHERE id = 1 + :q % 30000 AND state = 'new';
INSERT INTO account(id, email, name, created) VALUES (1 + :r % 40000, :s || '@upsert.test', :t, :i)
  ON CONFLICT(id) DO UPDATE SET name = excluded.name, tier = (tier + 1) % 10;
DELETE FROM line WHERE purchase_id = 1 + :r % 30000 AND qty = 1;
INSERT INTO audit(at, what, ref) VALUES (:i, 'tick', :r) RETURNING rowid;
@@bind 2000
INSERT OR IGNORE INTO account(id, email, name, created) VALUES (1 + :q % 40000, :s || '@ignore.test', :t, :i);
INSERT OR REPLACE INTO line(purchase_id, sku, qty, price) VALUES (1 + :r % 30000, 'sku-' || (:q % 50), 2, 1.5);
UPDATE OR IGNORE account SET email = :s || '@example.test' WHERE id = 1 + :q % 20000;
DELETE FROM audit WHERE rowid = 1 + :r % 50000;
@@bind 300
DELETE FROM account WHERE id = 1 + :r % 40000;
@@other
BEGIN;
SELECT count(*), sum(balance) FROM account;
@@merge 3000 16
INSERT INTO audit(at, what, ref) VALUES (:i, 'while read', :q);
UPDATE account SET balance = balance + 1 WHERE id = 1 + :r % 20000;
@@run
PRAGMA wal_checkpoint(PASSIVE);
@@other
SELECT count(*) FROM audit;
COMMIT;
SELECT count(*) FROM audit;
SELECT what, count(*) FROM audit GROUP BY what;
@@run
PRAGMA wal_checkpoint(TRUNCATE);
BEGIN;
UPDATE account SET tier = 0 WHERE tier = 9;
DELETE FROM audit WHERE what = 'tick' AND at % 2 = 0;
ROLLBACK;
BEGIN IMMEDIATE;
UPDATE account SET tier = 1 WHERE tier = 9;
SAVEPOINT a;
DELETE FROM audit WHERE what = 'tick' AND at % 3 = 0;
ROLLBACK TO a;
DELETE FROM audit WHERE what = 'tick' AND at % 5 = 0;
RELEASE a;
COMMIT;
PRAGMA foreign_key_check;
PRAGMA optimize;
SELECT count(*), sum(amount) FROM purchase;
SELECT count(*), sum(qty) FROM line;
@@open 300
PRAGMA foreign_keys=ON;
SELECT id, name, balance FROM account WHERE id = 1 + :r % 20000;
INSERT INTO audit(at, what, ref) VALUES (:i, 'open', :r);

@@ journal
CREATE TABLE event(id INTEGER PRIMARY KEY, kind TEXT NOT NULL, at INTEGER NOT NULL, body TEXT, weight REAL);
CREATE INDEX event_kind ON event(kind, at);
@@bind 2000
INSERT INTO event(kind, at, body, weight) VALUES (substr(:s, 1, 3), :r, :t, :f);
@@merge 30000 50
INSERT INTO event(kind, at, body, weight) VALUES (substr(:s, 1, 3), :r, :t, :f);
@@bind 2000
UPDATE event SET weight = weight * 1.01 WHERE id = 1 + :r % 32000;
DELETE FROM event WHERE id = 1 + :q % 32000 AND weight < 1000;
SELECT kind, at, body FROM event WHERE kind = substr(:s, 1, 3) AND at > :q ORDER BY at LIMIT 3;
@@run
BEGIN EXCLUSIVE;
UPDATE event SET weight = weight + 1;
COMMIT;
BEGIN;
DELETE FROM event WHERE id % 11 = 0;
INSERT INTO event(kind, at, body, weight) SELECT kind, at + 1, body, weight / 2 FROM event WHERE id % 13 = 0;
ROLLBACK;
ALTER TABLE event ADD COLUMN seen INTEGER DEFAULT 0;
UPDATE event SET seen = 1 WHERE id % 4 = 0;
ALTER TABLE event RENAME COLUMN body TO words;
ALTER TABLE event RENAME TO happening;
CREATE INDEX happening_weight ON happening(weight);
ALTER TABLE happening DROP COLUMN seen;
ANALYZE;
SELECT kind, count(*) FROM happening WHERE weight BETWEEN 1000 AND 50000 GROUP BY kind;
SELECT count(*) FROM happening WHERE kind = 'ced' AND at < 1000000000;
REINDEX;
PRAGMA integrity_check;
PRAGMA quick_check;
PRAGMA page_count;
PRAGMA freelist_count;
DELETE FROM happening WHERE id % 3 = 0;
VACUUM;
PRAGMA journal_mode=TRUNCATE;
UPDATE happening SET weight = weight - 1 WHERE id % 5 = 0;
PRAGMA journal_mode=PERSIST;
UPDATE happening SET weight = weight - 1 WHERE id % 7 = 0;
PRAGMA journal_mode=MEMORY;
UPDATE happening SET weight = weight - 1 WHERE id % 9 = 0;
PRAGMA journal_mode=DELETE;
PRAGMA auto_vacuum=INCREMENTAL;
VACUUM;
DELETE FROM happening WHERE id % 2 = 0;
PRAGMA incremental_vacuum(40);
PRAGMA cache_size=-200;
UPDATE happening SET words = words || ' again';
PRAGMA cache_size=-2000;
ATTACH ':memory:' AS side;
CREATE TABLE side.copy AS SELECT * FROM happening WHERE id % 7 = 1;
SELECT count(*), sum(c.weight) FROM side.copy c JOIN happening h ON h.id = c.id;
INSERT INTO happening(kind, at, words, weight) SELECT kind, at, words, weight FROM side.copy;
DETACH side;
CREATE TEMP TABLE scratch AS SELECT kind, count(*) AS n FROM happening GROUP BY kind;
CREATE INDEX temp.scratch_n ON scratch(n);
SELECT kind, n FROM scratch ORDER BY n DESC LIMIT 5;
DROP TABLE scratch;
PRAGMA integrity_check;
SELECT count(*), sum(weight), sum(length(words)) FROM happening;

@@ query memory
CREATE TABLE city(id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE, region TEXT NOT NULL, pop INTEGER, area REAL);
CREATE TABLE sale(id INTEGER PRIMARY KEY, city_id INTEGER NOT NULL, day INTEGER NOT NULL, item TEXT NOT NULL,
  units INTEGER NOT NULL, price REAL NOT NULL, tag TEXT);
CREATE TABLE node(id INTEGER PRIMARY KEY, parent INTEGER, label TEXT);
CREATE INDEX node_parent ON node(parent);
@@merge 500 500
INSERT INTO city VALUES (:i, :s, substr(:s, 1, 1), 1000 + :r % 5000000, (:q % 100000) / 10.0);
@@merge 150000 1000
INSERT INTO sale(city_id, day, item, units, price, tag)
  VALUES (1 + :r % 500, :q % 730, substr(:s, 1, instr(:s, '-') - 1), 1 + :q % 20, (:r % 20000) / 100.0,
    CASE WHEN :r % 5 = 0 THEN NULL ELSE substr(:t, 1, 12) END);
@@merge 5000 1000
INSERT INTO node VALUES (:i, CASE WHEN :i = 1 THEN NULL ELSE 1 + :r % (:i - 1) END, :s);
@@run
CREATE INDEX sale_city_day ON sale(city_id, day);
CREATE INDEX sale_item ON sale(item);
ANALYZE;
@@prepare 2
SELECT region, count(*), sum(pop), avg(area), min(name), max(name) FROM city GROUP BY region ORDER BY 2 DESC, 1;
SELECT c.region, s.item, sum(s.units * s.price) AS rev FROM sale s JOIN city c ON c.id = s.city_id
  GROUP BY c.region, s.item HAVING rev > 1000 ORDER BY rev DESC LIMIT 50;
SELECT day / 30 AS m, count(DISTINCT city_id), total(units), group_concat(DISTINCT item) FROM sale
  WHERE day BETWEEN 100 AND 400 GROUP BY m;
SELECT city_id, day, units,
    sum(units) OVER (PARTITION BY city_id ORDER BY day ROWS BETWEEN 6 PRECEDING AND CURRENT ROW),
    rank() OVER (PARTITION BY city_id ORDER BY units DESC),
    lag(units, 1, 0) OVER w, lead(units) OVER w, row_number() OVER w
  FROM sale WHERE city_id <= 60 WINDOW w AS (PARTITION BY city_id ORDER BY day, id);
SELECT item, day, dense_rank() OVER (ORDER BY item), ntile(4) OVER (PARTITION BY item ORDER BY price),
    first_value(price) OVER w, last_value(price) OVER w, nth_value(price, 3) OVER w,
    percent_rank() OVER (PARTITION BY item ORDER BY price), cume_dist() OVER (PARTITION BY item ORDER BY price),
    avg(price) FILTER (WHERE units > 5) OVER (PARTITION BY item),
    count(*) OVER (ORDER BY day RANGE BETWEEN 3 PRECEDING AND 3 FOLLOWING EXCLUDE CURRENT ROW),
    max(units) OVER (ORDER BY day GROUPS BETWEEN 1 PRECEDING AND 1 FOLLOWING)
  FROM sale WHERE city_id = 7 WINDOW w AS (PARTITION BY item ORDER BY day ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING);
WITH RECURSIVE down(id, depth, path) AS (
    SELECT id, 0, label FROM node WHERE parent IS NULL
    UNION ALL SELECT n.id, d.depth + 1, d.path || '/' || n.label FROM node n JOIN down d ON n.parent = d.id)
  SELECT depth, count(*), max(length(path)) FROM down GROUP BY depth;
WITH RECURSIVE up(id, parent, hops) AS (
    SELECT id, parent, 0 FROM node WHERE id > 4900
    UNION ALL SELECT n.id, n.parent, u.hops + 1 FROM node n JOIN up u ON n.id = u.parent)
  SELECT max(hops), count(*) FROM up;
WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM seq WHERE x < 20000),
    odd AS MATERIALIZED (SELECT x FROM seq WHERE x % 2 = 1)
  SELECT count(*), sum(o.x) FROM odd o JOIN seq s ON s.x = o.x + 1;
SELECT name FROM city WHERE id IN (SELECT city_id FROM sale WHERE units = 20 AND day < 30) ORDER BY name LIMIT 20;
SELECT count(*) FROM city WHERE id NOT IN (SELECT city_id FROM sale WHERE price > 199);
SELECT c.name, (SELECT count(*) FROM sale s WHERE s.city_id = c.id AND s.day = 100) AS n FROM city c
  WHERE EXISTS (SELECT 1 FROM sale s WHERE s.city_id = c.id AND s.units > 19 AND s.day > 700) ORDER BY n DESC, c.name LIMIT 30;
SELECT item, n FROM (SELECT item, count(*) AS n FROM sale GROUP BY item ORDER BY n DESC LIMIT 10) ORDER BY item;
SELECT item FROM sale WHERE day < 10 UNION SELECT item FROM sale WHERE day > 720 ORDER BY 1;
SELECT city_id FROM sale WHERE day = 1 INTERSECT SELECT city_id FROM sale WHERE day = 2
  EXCEPT SELECT city_id FROM sale WHERE day = 3 ORDER BY 1;
SELECT count(*) FROM (SELECT item, day FROM sale WHERE city_id < 50 UNION ALL SELECT region, pop FROM city);
SELECT DISTINCT item, units FROM sale WHERE city_id BETWEEN 10 AND 20 ORDER BY item DESC, units LIMIT 40 OFFSET 20;
SELECT c.id, c.name FROM city c LEFT JOIN sale s ON s.city_id = c.id AND s.day = 999 WHERE s.id IS NULL ORDER BY c.name LIMIT 10;
SELECT count(*), count(s.id), count(c.id) FROM sale s RIGHT JOIN city c ON c.id = s.city_id AND s.day = 5;
SELECT count(*), count(a.id), count(b.id) FROM (SELECT * FROM city WHERE id < 300) a
  FULL JOIN (SELECT * FROM city WHERE id > 200) b ON a.id = b.id;
SELECT a.item, b.item, count(*) FROM sale a JOIN sale b ON a.city_id = b.city_id AND a.day = b.day AND a.id < b.id
  WHERE a.city_id < 40 GROUP BY 1, 2 ORDER BY 3 DESC, 1, 2 LIMIT 10;
SELECT count(*) FROM sale a JOIN (SELECT DISTINCT tag FROM sale WHERE city_id = 3) b ON a.tag = b.tag WHERE a.city_id < 30;
SELECT city_id, day, id FROM sale WHERE (city_id, day) > (250, 365) ORDER BY city_id, day, id LIMIT 25;
SELECT count(*) FROM sale WHERE (city_id, day) IN (SELECT id, pop % 730 FROM city);
SELECT count(*), sum(units) FROM sale WHERE city_id = 5 OR item = 'cedar' OR day IN (1, 2, 3, 500, 729);
SELECT count(*) FROM sale WHERE item GLOB 'ce*' OR item LIKE 'w%';
SELECT count(*) FROM city WHERE name LIKE 'CE%' OR name = 'WILLOW-000123';
SELECT max(day), min(day) FROM sale WHERE city_id = 3;
SELECT median(price), percentile(price, 90), percentile_cont(price, 0.25), percentile_disc(price, 0.75) FROM sale WHERE city_id < 20;
SELECT item, string_agg(units, ',' ORDER BY units DESC), count(*) FILTER (WHERE tag IS NULL) FROM sale WHERE day = 77 GROUP BY item;
SELECT sum(length(item)), count(*) FROM (SELECT item FROM sale ORDER BY tag, price DESC, id);
SELECT id, price, units FROM sale ORDER BY price, units DESC, id LIMIT 50 OFFSET 60000;
SELECT tag, count(*) FROM sale GROUP BY tag ORDER BY count(*) DESC, tag NULLS LAST LIMIT 20;
SELECT name FROM city ORDER BY name COLLATE BINARY DESC, pop LIMIT 20;
SELECT sum(CASE WHEN units > 10 THEN price ELSE 0 END), sum(iif(tag IS NULL, 1, 0)), count(nullif(units, 1)),
    sum(coalesce(length(tag), -1)), sum(CAST(price AS INTEGER)), max(typeof(tag)), sum(units IS NOT DISTINCT FROM 3)
  FROM sale;
SELECT count(*) FROM sale WHERE CAST(units AS TEXT) = '7' AND price > '50' AND item < 5;
SELECT column1, column2 FROM (VALUES (1, 'a'), (2, 'b'), (3, 'c'), (4, NULL)) WHERE column2 IS NOT NULL ORDER BY column1 DESC;
SELECT count(*) FROM sale WHERE day IN (3, 14, 15, 92, 65, 35, 89, 79, 323, 84, 626, 433, 83, 27, 95, 28, 84, 197, 169, 399, 375, 105);
EXPLAIN QUERY PLAN SELECT c.region, sum(s.units) FROM sale s JOIN city c ON c.id = s.city_id WHERE s.day > 700 GROUP BY 1;
EXPLAIN SELECT * FROM sale WHERE city_id = 3 ORDER BY day;
@@run
CREATE TABLE gen(id INTEGER PRIMARY KEY, a INT NOT NULL, b INT NOT NULL, label TEXT,
  c INT GENERATED ALWAYS AS (a * b) VIRTUAL, d TEXT GENERATED ALWAYS AS (printf('%05d', a)) STORED) STRICT;
CREATE INDEX gen_sum ON gen(a + b);
CREATE INDEX gen_c ON gen(c);
CREATE UNIQUE INDEX gen_label ON gen(lower(label)) WHERE label IS NOT NULL;
INSERT INTO gen(a, b, label) SELECT units, city_id, CASE WHEN id % 3 = 0 THEN NULL ELSE 'L' || id END FROM sale WHERE id <= 30000;
CREATE TABLE summary AS SELECT city_id, item, sum(units) AS units, sum(units * price) AS revenue FROM sale GROUP BY city_id, item;
CREATE UNIQUE INDEX summary_key ON summary(city_id, item);
UPDATE summary SET revenue = round(revenue * (1 + c.pop / 1e8), 2) FROM city c WHERE c.id = summary.city_id;
DELETE FROM summary WHERE city_id IN (SELECT id FROM city WHERE pop < 100000);
INSERT INTO summary SELECT city_id, item || '!', units, revenue FROM summary WHERE units > 300
  ON CONFLICT DO NOTHING;
CREATE VIEW busy AS SELECT c.id, c.name, s.units FROM city c JOIN summary s ON s.city_id = c.id WHERE s.units > 200;
CREATE TABLE renamed(id INTEGER, old TEXT, new TEXT);
CREATE TRIGGER busy_rename INSTEAD OF UPDATE OF name ON busy BEGIN
  INSERT INTO renamed VALUES (old.id, old.name, new.name);
  UPDATE city SET name = new.name WHERE id = old.id;
END;
CREATE TRIGGER city_guard BEFORE UPDATE OF pop ON city WHEN new.pop < 0 BEGIN
  SELECT raise(IGNORE);
END;
CREATE TRIGGER city_gone BEFORE DELETE ON city BEGIN
  DELETE FROM sale WHERE city_id = old.id;
  DELETE FROM summary WHERE city_id = old.id;
END;
UPDATE busy SET name = name || '*' WHERE id % 50 = 0;
UPDATE city SET pop = -pop WHERE id % 9 = 0;
UPDATE city SET pop = pop + 1 WHERE id % 9 = 1;
DELETE FROM city WHERE id > 495;
@@bind 3000
SELECT id, c, d FROM gen WHERE a + b = 20 + :r % 400 LIMIT 5;
SELECT count(*) FROM gen WHERE c BETWEEN :r % 5000 AND :r % 5000 + 50;
SELECT id FROM gen WHERE lower(label) = 'l' || (1 + :r % 30000);
SELECT units, revenue FROM summary WHERE city_id = 1 + :r % 495 AND item = substr(:s, 1, instr(:s, '-') - 1);
SELECT name, units FROM busy WHERE id = 1 + :q % 495 ORDER BY units DESC LIMIT 3;
@@run
SELECT count(*), sum(c), sum(length(d)) FROM gen;
SELECT count(*), sum(units), sum(revenue) FROM summary;
SELECT count(*), sum(pop) FROM city;
SELECT count(*) FROM renamed;
SELECT count(*), sum(units) FROM sale;

@@ text memory
CREATE TABLE note(id INTEGER PRIMARY KEY, title TEXT, body TEXT, at INTEGER, score REAL, raw BLOB);
@@merge 30000 1000
INSERT INTO note VALUES (:i, :s, :t, 1500000000 + :r % 300000000, :f, :b);
@@prepare 2
SELECT sum(length(body)), sum(length(raw)), sum(octet_length(title)), sum(instr(body, 'river')), sum(unicode(body)) FROM note;
SELECT count(*) FROM note WHERE body LIKE '%harbor%';
SELECT count(*) FROM note WHERE body LIKE 'amber%' OR body LIKE '%_ello_' OR title LIKE 'CEDAR-0%';
SELECT count(*) FROM note WHERE body GLOB '*[xyz]e*' AND body NOT GLOB 'a*';
SELECT count(*) FROM note WHERE body LIKE '%w!_%' ESCAPE '!' OR title LIKE '%-00%';
SELECT sum(length(upper(body))), sum(length(lower(title))), sum(length(trim(body, 'abcdef '))), sum(length(ltrim(title, 'abc'))),
    sum(length(rtrim(title, '0123456789'))) FROM note;
SELECT sum(length(replace(body, 'river', 'stream'))), sum(length(substr(body, 5, 20))), sum(length(substr(body, -8))),
    sum(length(substring(title, 2))) FROM note;
SELECT sum(length(printf('%08d|%-12s|%10.3f|%x|%e|%g|%c|%5.2f%%|%q|%Q|%w', id, title, score, at, score, score, 65 + id % 26, score / 1000, body, title, title))) FROM note;
SELECT sum(length(format('%,d %+d %o %X %.0f %s', at, id, id, at, score, NULL))) FROM note;
SELECT sum(length(hex(raw))), sum(length(unhex(hex(raw)))), sum(length(quote(raw))), sum(length(quote(body))), sum(length(quote(score))) FROM note;
SELECT sum(length(title || ' ' || body)), sum(length(concat(title, id, score))), sum(length(concat_ws('/', title, NULL, id))) FROM note;
SELECT sum(length(char(65 + id % 26, 228, 8364, 128512))), sum(unicode(substr(body, 3))) FROM note WHERE id < 5000;
SELECT count(*) FROM note WHERE title < body AND title COLLATE NOCASE > 'M' AND body COLLATE RTRIM != title;
SELECT sum(score > 50000), sum(abs(score - 70000)), sum(round(score, 2)), sum(round(score)), sum(sign(score - 1000)),
    max(score, at), min(id, at), sum(CAST(score AS INTEGER) % 7), sum(id / 3), sum(id % 5), sum(id * 1.5), sum(-score) FROM note;
SELECT sum(CAST(title AS REAL)), sum(CAST(CAST(score AS TEXT) AS REAL)), sum(CAST(id AS TEXT) + 0), sum(length(CAST(score AS TEXT))),
    sum(CAST('0x1F' AS INTEGER)), sum(CAST(' 12.5e2xyz' AS REAL)), sum('1e3' + 0), sum(CAST(substr(title, -6) AS INTEGER)) FROM note;
SELECT sum(length(date(at, 'unixepoch'))), sum(length(time(at, 'unixepoch'))), sum(length(datetime(at, 'unixepoch', '+1 month', 'start of month', '-1 day'))),
    sum(julianday(at, 'unixepoch')), sum(unixepoch(datetime(at, 'unixepoch', 'weekday 0'))),
    sum(length(strftime('%Y-%m-%dT%H:%M:%fZ %j %w %W %s %u %G-%V %e %k %I %p %R %T %F', at, 'unixepoch'))) FROM note WHERE id < 12000;
SELECT sum(unixepoch('2024-02-29 12:34:56', '+' || (id % 400) || ' days', '+' || (id % 24) || ' hours')),
    sum(length(timediff(datetime(at, 'unixepoch'), '2020-01-01'))), sum(julianday('2025-06-01') - julianday(date(at, 'unixepoch'))),
    sum(length(date(at, 'unixepoch', 'start of year', '+6 months', 'subsec'))), sum(strftime('%s', at, 'unixepoch', 'start of day') + 0)
  FROM note WHERE id < 12000;
SELECT count(date('now')), count(datetime('now', 'localtime')), count(strftime('%s', 'now', 'utc')), count(unixepoch('now', 'subsec'));
SELECT sum(length(zeroblob(id % 100))), sum(length(substr(raw, 2, 5))), sum(length(raw || x'00ff')), sum(instr(raw, x'00')), sum(raw > x'80'),
    sum(length(randomblob(4))), count(random()), sum(typeof(raw) = 'blob') FROM note;
SELECT sum(likelihood(id > 5, 0.9)), sum(likely(id < 5)), sum(unlikely(score < 0)), count(sqlite_version()), sum(changes()), sum(total_changes()),
    count(sqlite_source_id()), sum(last_insert_rowid()), sum(length(typeof(id))), count(sqlite_compileoption_used('ENABLE_FTS5')) FROM note WHERE id < 2000;
SELECT title, count(*) FROM note GROUP BY substr(title, 1, 4) ORDER BY 2 DESC, 1 LIMIT 5;
SELECT body FROM note ORDER BY body COLLATE NOCASE, id LIMIT 5 OFFSET 15000;
SELECT sum(length(g)) FROM (SELECT group_concat(substr(body, 1, 6), ';') AS g FROM note GROUP BY id % 100);
SELECT min(body), max(body), min(title COLLATE NOCASE), count(DISTINCT substr(body, 1, 5)) FROM note;
@@bind 20000
SELECT id, title, body, at, score, raw FROM note WHERE id = 1 + :r % 30000;
SELECT :t || '!' , length(:t), upper(:s), :f * 2, :b, :n, :i + 1, abs(:r - :q), typeof(:n), coalesce(:n, :s);

@@ json memory
CREATE TABLE doc(id INTEGER PRIMARY KEY, j TEXT, b BLOB);
@@merge 20000 500
INSERT INTO doc(id, j) VALUES (:i, json_object('id', :i, 'name', :s, 'tags', json_array(substr(:t, 1, 5), substr(:t, 7, 6), :r % 10),
  'geo', json_object('lat', (:r % 180000) / 1000.0 - 90, 'lon', (:q % 360000) / 1000.0 - 180), 'active', json(iif(:q % 2, 'true', 'false')),
  'score', :f, 'note', CASE WHEN :r % 4 = 0 THEN NULL ELSE :t END, 'history', json_array(json_object('at', :r, 'by', :s), json_object('at', :q, 'by', 'sys'))));
@@run
UPDATE doc SET b = jsonb(j);
CREATE INDEX doc_name ON doc(j ->> '$.name');
@@prepare 2
SELECT sum(json_extract(j, '$.id')), sum(j ->> '$.geo.lat'), count(j -> '$.tags'), sum(length(j ->> 'name')), sum(json_extract(j, '$.tags[2]')),
    sum(json_array_length(j, '$.tags')), sum(json_type(j, '$.note') = 'null'), sum(json_extract(j, '$.active')), sum(j ->> '$.history[1].at') FROM doc;
SELECT sum(json_extract(b, '$.id')), sum(b ->> '$.geo.lon'), sum(length(b ->> '$.name')), sum(json_array_length(b, '$.history')),
    sum(length(json(b))), sum(json_valid(b, 8)), sum(length(json_extract(b, '$.tags[#-1]', '$.note'))) FROM doc;
SELECT sum(length(json_set(j, '$.score', 1, '$.extra', 'x'))), sum(length(json_insert(j, '$.tags[#]', 'new'))), sum(length(json_replace(j, '$.name', id))),
    sum(length(json_remove(j, '$.history', '$.note'))), sum(length(json_patch(j, '{"geo":null,"z":[1,2]}'))) FROM doc WHERE id <= 8000;
SELECT sum(length(jsonb_set(b, '$.score', 2))), sum(length(jsonb_remove(b, '$.tags'))), sum(length(jsonb_insert(b, '$.n', id))),
    sum(length(jsonb_patch(b, '{"a":1}'))), sum(length(jsonb_extract(b, '$.geo'))), sum(length(jsonb_replace(b, '$.id', 0))) FROM doc WHERE id <= 8000;
SELECT count(*), sum(e.value = 'cedar'), sum(e.type = 'integer') FROM doc, json_each(doc.j, '$.tags') e WHERE doc.id <= 10000;
SELECT e.key, count(*), sum(e.atom IS NULL) FROM doc, json_tree(doc.j) e WHERE doc.id <= 3000 GROUP BY e.key ORDER BY 2 DESC, 1 LIMIT 12;
SELECT count(*), sum(e.id), max(e.fullkey), max(e.path) FROM doc, jsonb_tree(doc.b, '$.history') e WHERE doc.id <= 3000 AND e.type != 'object';
SELECT sum(length(a)), sum(length(o)), sum(length(b)) FROM (SELECT json_group_array(j ->> '$.name') AS a, json_group_object(id, j -> '$.geo') AS o,
    jsonb_group_array(id) AS b FROM doc GROUP BY id % 50);
SELECT sum(length(json_pretty(j))), sum(length(json_quote(j ->> '$.name'))), sum(json_valid(j)), sum(json_error_position(j)),
    sum(json_valid(substr(j, 1, 40))), sum(json_error_position(substr(j, 1, 40))) FROM doc WHERE id <= 5000;
SELECT sum(json_extract(json_object('a', id, 'b', json_array(id, j ->> '$.name'), 'c', j -> '$.geo'), '$.b[0]')),
    sum(json_array_length(json_array(1, id, json_object('k', id)))), count(json_extract(json_set(j, '$.z', id), '$.z')) FROM doc;
SELECT sum(json_extract(made, '$.a')), count(json_extract(made, '$.n')), sum(json_array_length(made, '$.l'))
  FROM (SELECT json_object('a', id, 'n', j ->> '$.name', 'l', json_array(id, id + 1), 'note', j ->> '$.note', 'geo', j -> '$.geo') AS made FROM doc);
SELECT id FROM doc WHERE j ->> '$.name' = 'cedar-000123' OR j ->> '$.name' BETWEEN 'r' AND 's' ORDER BY j ->> '$.name' LIMIT 20;
SELECT count(*) FROM doc WHERE json_extract(j, '$.geo.lat') > 0 AND j ->> '$.active' AND json_extract(j, '$.tags[0]') LIKE 'a%';
SELECT sum(length(json('{"a":[1,2.5,-3e4,"xé\n",true,false,null,{"b":{}}], "c":"😀"}'))),
    sum(length(json(' [ 1 , 2 , { "k" : "v" } ] '))), sum(json('[1,2' || id || ']') IS NOT NULL),
    sum(length(json_array(id, 1.5, 'text', NULL, json_object('k', id), x'00' IS NULL))), sum(length(json5.x))
  FROM doc, (SELECT json('{a:1,b:''two'',c:[0x10,+1,.5,Infinity,],/*c*/d:NaN}') AS x) AS json5 WHERE id <= 5000;
@@bind 8000
SELECT j ->> '$.name', j -> '$.geo', json_extract(j, '$.tags[0]', '$.score') FROM doc WHERE id = 1 + :r % 20000;
UPDATE doc SET j = json_set(j, '$.score', :f, '$.seen', :i) WHERE id = 1 + :q % 20000;
SELECT id FROM doc WHERE j ->> '$.name' = :s;
SELECT json_object('i', :i, 's', :s, 't', json_array(:t, :f, :n)), json_valid(:t), json_quote(:s) -> '$', json_quote(:t);

@@ search
PRAGMA journal_mode=WAL;
CREATE VIRTUAL TABLE page USING fts5(title, body, tokenize = 'porter unicode61 remove_diacritics 2');
CREATE VIRTUAL TABLE code USING fts5(text, tokenize = 'trigram');
CREATE TABLE article(id INTEGER PRIMARY KEY, title TEXT, body TEXT);
CREATE VIRTUAL TABLE article_fts USING fts5(title, body, content = 'article', content_rowid = 'id', prefix = '2 3');
CREATE TRIGGER article_ai AFTER INSERT ON article BEGIN
  INSERT INTO article_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER article_ad AFTER DELETE ON article BEGIN
  INSERT INTO article_fts(article_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
END;
CREATE TRIGGER article_au AFTER UPDATE ON article BEGIN
  INSERT INTO article_fts(article_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
  INSERT INTO article_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE VIRTUAL TABLE page_terms USING fts5vocab(page, row);
@@merge 20000 100
INSERT INTO page(rowid, title, body) VALUES (:i, :s, :t || ' ' || :s || ' running jumped cities river riverside rivers');
INSERT INTO article(id, title, body) VALUES (:i, :s, :t);
@@merge 3000 100
INSERT INTO code(text) VALUES (:t);
@@bind 3000
SELECT rowid, title FROM page WHERE page MATCH substr(:s, 1, instr(:s, '-') - 1) ORDER BY rank LIMIT 10;
SELECT rowid, bm25(page, 10.0, 1.0) FROM page WHERE page MATCH 'body: (river OR harbor) NOT kelp' ORDER BY bm25(page, 10.0, 1.0) LIMIT 5;
SELECT count(*) FROM page WHERE page MATCH '"' || substr(:t, 1, instr(:t, ' ') - 1) || '" + ' || substr(:s, 1, 3) || '*';
SELECT rowid FROM article_fts WHERE article_fts MATCH substr(:s, 1, 2) || '* AND ' || substr(:t, 1, 3) || '*' LIMIT 20;
@@bind 300
SELECT highlight(page, 1, '[', ']'), snippet(page, 1, '<b>', '</b>', '...', 8) FROM page WHERE page MATCH 'NEAR(amber cedar, 6) OR quartz' LIMIT 20;
SELECT count(*) FROM page WHERE page MATCH 'title: ' || substr(:s, 1, instr(:s, '-') - 1) || ' AND run';
SELECT count(*) FROM page WHERE page MATCH substr(:s, 1, 3) || '*';
SELECT count(*) FROM page WHERE page MATCH (:r % 10) || (:q % 10) || '*';
SELECT count(*) FROM page WHERE page MATCH substr(:s, 1, 2) || '* AND ' || substr(:t, 1, 1) || '*';
SELECT rowid FROM page WHERE page MATCH '({title body}: jump* AND (city OR willow)) NOT ' || substr(:s, 1, instr(:s, '-') - 1) ORDER BY rowid DESC LIMIT 10;
SELECT count(*) FROM code WHERE text LIKE '%' || substr(:t, 3, 6) || '%';
SELECT count(*) FROM code WHERE code MATCH '"' || substr(:t, 2, 5) || '"';
SELECT count(*) FROM code WHERE text GLOB '*' || substr(:t, 4, 4) || '*';
SELECT term, doc, cnt FROM page_terms WHERE term >= substr(:s, 1, 2) ORDER BY term LIMIT 5;
SELECT a.id, a.title FROM article_fts f JOIN article a ON a.id = f.rowid WHERE article_fts MATCH 'body: ' || substr(:t, 1, instr(:t, ' ') - 1) ORDER BY f.rank LIMIT 5;
@@merge 4000 20
UPDATE page SET body = :t WHERE rowid = 1 + :r % 20000;
DELETE FROM page WHERE rowid = 1 + :q % 20000;
UPDATE article SET body = :t || ' edited' WHERE id = 1 + :r % 20000;
DELETE FROM article WHERE id = 1 + :q % 20000;
INSERT INTO article(title, body) VALUES (:s, :t);
@@run
INSERT INTO page(page, rank) VALUES ('merge', 8);
INSERT INTO page(page) VALUES ('optimize');
INSERT INTO article_fts(article_fts) VALUES ('integrity-check');
INSERT INTO page(page, rank) VALUES ('rank', 'bm25(2.0, 1.0)');
INSERT INTO article_fts(article_fts) VALUES ('rebuild');
SELECT count(*) FROM page WHERE page MATCH 'river';
SELECT count(*), sum(length(body)) FROM page;
SELECT count(*) FROM article_fts WHERE article_fts MATCH 'edited';
SELECT count(*), sum(cnt) FROM page_terms;
PRAGMA wal_checkpoint(TRUNCATE);

@@ space memory
CREATE VIRTUAL TABLE area USING rtree(id, minx, maxx, miny, maxy, +label TEXT);
CREATE VIRTUAL TABLE cell USING rtree_i32(id, x0, x1, y0, y1, z0, z1);
CREATE TABLE place(id INTEGER PRIMARY KEY, name TEXT, x REAL, y REAL);
@@merge 30000 500
INSERT INTO area VALUES (:i, (:r % 100000) / 10.0, (:r % 100000) / 10.0 + (:q % 50) / 10.0, (:q % 100000) / 10.0, (:q % 100000) / 10.0 + (:r % 70) / 10.0, :s);
INSERT INTO cell VALUES (:i, :r % 1000, :r % 1000 + :q % 9, :q % 1000, :q % 1000 + :r % 9, :i % 50, :i % 50 + 3);
INSERT INTO place VALUES (:i, :s, (:q % 100000) / 10.0, (:r % 100000) / 10.0);
@@bind 6000
SELECT id, label FROM area WHERE minx >= :r % 9000 AND maxx <= :r % 9000 + 400 AND miny >= :q % 9000 AND maxy <= :q % 9000 + 400;
SELECT count(*) FROM area WHERE maxx >= :q % 10000 AND minx <= :q % 10000 + 30 AND maxy >= :r % 10000 AND miny <= :r % 10000 + 30;
SELECT count(*) FROM cell WHERE x1 >= :r % 990 AND x0 <= :r % 990 + 12 AND y1 >= :q % 990 AND y0 <= :q % 990 + 12 AND z0 < 25;
SELECT id, minx, maxy FROM area WHERE id = 1 + :r % 30000;
@@bind 400
SELECT p.name, count(*) FROM place p JOIN area a ON a.minx <= p.x AND a.maxx >= p.x AND a.miny <= p.y AND a.maxy >= p.y
  WHERE p.id BETWEEN 1 + :r % 29000 AND 20 + :r % 29000 GROUP BY p.id;
@@merge 6000 50
UPDATE area SET maxx = maxx + 1, maxy = maxy + 1 WHERE id = 1 + :r % 30000;
DELETE FROM area WHERE id = 1 + :q % 30000;
DELETE FROM cell WHERE id = 1 + :r % 30000;
INSERT INTO cell VALUES (40000 + :i, :q % 1000, :q % 1000 + 2, :r % 1000, :r % 1000 + 2, 1, 2);
@@run
SELECT rtreecheck('area'), rtreecheck('cell');
SELECT count(*), sum(minx), sum(maxy) FROM area;
SELECT count(*), sum(x0), sum(z1) FROM cell;
SELECT count(*) FROM area_node;

@@ number memory
CREATE TABLE sample(id INTEGER PRIMARY KEY, x REAL, y REAL, n INTEGER, s TEXT);
@@merge 60000 1000
INSERT INTO sample VALUES (:i, :f / 1000.0, ((:q % 2000000) - 1000000) / 997.0, :r - 1073741824, :f);
@@prepare 2
SELECT sum(sin(x)), sum(cos(y)), sum(tan(x / 100)), sum(asin(x / 200)), sum(acos(y / 2000)), sum(atan(y)), sum(atan2(y, x + 1)) FROM sample;
SELECT sum(sinh(x / 50)), sum(cosh(y / 500)), sum(tanh(y)), sum(asinh(y)), sum(acosh(x + 1)), sum(atanh(y / 2000)) FROM sample;
SELECT sum(exp(x / 20)), sum(ln(x + 1)), sum(log(x + 1)), sum(log10(x + 1)), sum(log2(x + 1)), sum(log(3, x + 2)), sum(pow(x, 1.5)), sum(power(1.0001, n % 1000)),
    sum(sqrt(x)) FROM sample;
SELECT sum(ceil(y)), sum(ceiling(x)), sum(floor(y)), sum(trunc(y)), sum(mod(n, 97)), sum(mod(y, 7.5)), sum(degrees(y)), sum(radians(x)), sum(pi() * x),
    sum(round(y, 3)), sum(abs(n)), sum(sign(n)) FROM sample;
SELECT total(n * n), total(n * 4294967296), total(n), avg(n), sum(n / 7), sum(n % 1000), sum(n & 255), sum(n | 1), sum(n << 3), sum(n >> 2), sum(~n),
    sum(-n), sum(n = x), sum(n < y), sum(x BETWEEN 10 AND 20), max(n), min(n) FROM sample;
SELECT sum(length(CAST(x AS TEXT))), sum(length(CAST(y AS TEXT))), sum(length(printf('%.15g', y))), sum(length(printf('%!.20e', x))),
    sum(length(printf('%.3f', y * 1e12))), sum(length(CAST(n AS TEXT))), sum(length(format('%.2f', x))), sum(length(x || '')) FROM sample;
SELECT sum(CAST(s AS REAL)), sum(CAST(CAST(y AS TEXT) AS REAL) = y), sum(CAST(s AS INTEGER)), sum(s + 0), sum(CAST(n AS REAL)), total(CAST(y * 1e15 AS INTEGER)),
    sum(CAST(printf('%de%d', n % 1000, n % 300) AS REAL) > 0), sum(s = x), sum(s < '5') FROM sample;
SELECT count(*), sum(x), avg(y) FROM sample WHERE x > 30.5 AND y < 100.25 AND n > 0 AND x * y < 1e6;
SELECT n % 10 AS d, count(*), sum(x), avg(y), min(x), max(y), total(n) FROM sample GROUP BY d ORDER BY d;
SELECT id, x FROM sample ORDER BY y DESC, x LIMIT 20;
SELECT id, y FROM sample ORDER BY abs(y), id LIMIT 20 OFFSET 30000;
SELECT sum(x > y), sum(x = y), sum(n > x), sum(n < 0.5), sum(9223372036854775807 + n > 0), sum(9007199254740993 = n + 9007199254740993.0),
    sum(typeof(n + 9223372036854775807) = 'real'), sum(typeof(n * 3) = 'integer') FROM sample;
@@bind 10000
SELECT :f * 1.5 + :r, round(:f, 2), CAST(:f AS INTEGER), :r / 7, :r % 7, :f > :r, sqrt(:f), printf('%.4f', :f);

@@ large
PRAGMA journal_mode=WAL;
PRAGMA cache_size=-800;
CREATE TABLE file(id INTEGER PRIMARY KEY, name TEXT NOT NULL, mime TEXT, data BLOB, text TEXT);
CREATE INDEX file_name ON file(name);
@@merge 1500 10
INSERT INTO file VALUES (:i, :s || '/' || :t, 'application/octet-stream', zeroblob(200 + :r % 30000), NULL);
INSERT INTO file VALUES (10000 + :i, :t || '/' || :s || '/' || :t, 'text/plain', NULL,
  printf('%.*c', 100 + :q % 20000, 'x') || :t);
INSERT INTO file VALUES (20000 + :i, :s, 'small', :b, :t);
@@bind 1500
SELECT id, name, mime, data, text FROM file WHERE id = 1 + :r % 1500;
SELECT length(data), length(text), substr(text, -30), substr(data, 100, 16) FROM file WHERE id = 10001 + :q % 1500;
SELECT id FROM file WHERE name = :s;
UPDATE file SET data = zeroblob(300 + :q % 20000) WHERE id = 1 + :q % 1500;
UPDATE file SET text = text || :s WHERE id = 10001 + :r % 1500;
@@merge 8 2
UPDATE file SET mime = mime || '.' WHERE id % 4 = :i % 4;
DELETE FROM file WHERE id % 97 = :i;
@@run
SELECT count(*), sum(length(data)), sum(length(text)), max(length(name)) FROM file;
SELECT mime, count(*), sum(length(coalesce(data, text))) FROM file GROUP BY mime ORDER BY mime;
SELECT sum(length(name)) FROM (SELECT name FROM file ORDER BY text, name);
CREATE INDEX file_text ON file(substr(text, 1, 600));
DELETE FROM file WHERE id % 3 = 0;
PRAGMA wal_checkpoint(FULL);
VACUUM;
PRAGMA journal_mode=DELETE;
BEGIN;
UPDATE file SET mime = 'x-' || mime;
DELETE FROM file WHERE id % 5 = 0;
COMMIT;
BEGIN;
UPDATE file SET name = name || '.bak';
SAVEPOINT s;
DELETE FROM file WHERE id % 2 = 0;
ROLLBACK TO s;
UPDATE file SET mime = NULL WHERE id % 7 = 0;
RELEASE s;
ROLLBACK;
PRAGMA integrity_check;
SELECT count(*), sum(length(data)), sum(length(text)) FROM file;

@@ schema
PRAGMA journal_mode=WAL;
PRAGMA user_version=7;
PRAGMA application_id=1819898995;
CREATE TABLE tenant(id INTEGER PRIMARY KEY, slug TEXT NOT NULL UNIQUE, plan TEXT NOT NULL DEFAULT 'free', created TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE member(id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL REFERENCES tenant(id) ON DELETE CASCADE, email TEXT NOT NULL COLLATE NOCASE,
  role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'user')), joined INTEGER, UNIQUE (tenant_id, email));
CREATE TABLE project(id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL REFERENCES tenant(id) ON DELETE CASCADE, name TEXT NOT NULL,
  archived INTEGER NOT NULL DEFAULT 0, settings TEXT DEFAULT '{}', UNIQUE (tenant_id, name));
CREATE TABLE task(id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE, assignee INTEGER REFERENCES member(id) ON DELETE SET NULL,
  title TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'open', priority INTEGER NOT NULL DEFAULT 3, due TEXT, done_at TEXT, estimate REAL);
CREATE TABLE comment(id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE, author INTEGER REFERENCES member(id) ON DELETE SET NULL, body TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE label(id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, name TEXT NOT NULL, color TEXT);
CREATE TABLE task_label(task_id INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE, label_id INTEGER NOT NULL REFERENCES label(id) ON DELETE CASCADE, PRIMARY KEY (task_id, label_id)) WITHOUT ROWID;
CREATE TABLE session(token BLOB PRIMARY KEY, member_id INTEGER NOT NULL, expires INTEGER NOT NULL, data TEXT) WITHOUT ROWID;
CREATE TABLE setting(scope TEXT NOT NULL, key TEXT NOT NULL, value, PRIMARY KEY (scope, key));
CREATE TABLE migration(version INTEGER PRIMARY KEY, name TEXT, applied TEXT DEFAULT (datetime('now')));
CREATE INDEX member_tenant ON member(tenant_id, role);
CREATE INDEX task_project_state ON task(project_id, state, priority DESC);
CREATE INDEX task_assignee ON task(assignee) WHERE state != 'done';
CREATE INDEX task_due ON task(due) WHERE due IS NOT NULL;
CREATE INDEX comment_task ON comment(task_id, at);
CREATE INDEX label_tenant ON label(tenant_id, name COLLATE NOCASE);
CREATE INDEX session_expires ON session(expires);
CREATE INDEX task_title ON task(lower(title));
CREATE VIEW open_task AS SELECT t.*, p.tenant_id, p.name AS project FROM task t JOIN project p ON p.id = t.project_id WHERE t.state != 'done' AND NOT p.archived;
CREATE VIEW member_load AS SELECT m.id, m.email, count(t.id) AS open, coalesce(sum(t.estimate), 0) AS hours FROM member m LEFT JOIN task t ON t.assignee = m.id AND t.state != 'done' GROUP BY m.id;
CREATE TRIGGER task_done AFTER UPDATE OF state ON task WHEN new.state = 'done' AND old.state != 'done' BEGIN
  UPDATE task SET done_at = '2026-01-01 00:00:00' WHERE id = new.id;
END;
CREATE TRIGGER project_archive AFTER UPDATE OF archived ON project WHEN new.archived BEGIN
  UPDATE task SET state = 'done' WHERE project_id = new.id AND state != 'done';
END;
CREATE TRIGGER member_check BEFORE INSERT ON member WHEN new.email NOT LIKE '%@%' BEGIN
  SELECT raise(ABORT, 'not an email');
END;
PRAGMA foreign_keys=ON;
@@merge 50 50
INSERT INTO tenant(id, slug, plan) VALUES (:i, :s, CASE :r % 3 WHEN 0 THEN 'free' WHEN 1 THEN 'team' ELSE 'pro' END);
INSERT INTO migration(version, name) VALUES (:i, :s);
@@merge 1000 50
INSERT INTO member(id, tenant_id, email, role, joined) VALUES (:i, 1 + :r % 50, :s || '@Example.test', CASE WHEN :q % 10 = 0 THEN 'admin' ELSE 'user' END, :q);
INSERT INTO project(id, tenant_id, name) VALUES (:i, 1 + :q % 50, :s);
INSERT INTO label(id, tenant_id, name, color) VALUES (:i, 1 + :r % 50, substr(:s, 1, 6), printf('#%06x', :q % 16777216));
INSERT INTO session(token, member_id, expires, data) VALUES (:b, 1 + :r % 1000, :q, :t);
INSERT INTO setting(scope, key, value) VALUES ('tenant:' || (:r % 50), :s, :f) ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value;
INSERT INTO member(tenant_id, email, role) VALUES (1, :s, 'user');
@@merge 20000 25
INSERT INTO task(id, project_id, assignee, title, state, priority, due, estimate)
  VALUES (:i, 1 + :r % 1000, CASE WHEN :q % 4 = 0 THEN NULL ELSE 1 + :q % 1000 END, :t, CASE :r % 5 WHEN 0 THEN 'done' WHEN 1 THEN 'doing' ELSE 'open' END,
    1 + :q % 5, CASE WHEN :r % 3 = 0 THEN NULL ELSE date(1750000000 + :q % 30000000, 'unixepoch') END, (:r % 80) / 4.0);
INSERT INTO comment(task_id, author, body, at) VALUES (:i, 1 + :r % 1000, :t, 1750000000 + :i);
INSERT OR IGNORE INTO task_label(task_id, label_id) VALUES (:i, 1 + :q % 1000);
INSERT OR IGNORE INTO task_label(task_id, label_id) VALUES (1 + :r % :i, 1 + :r % 1000);
@@prepare 150
SELECT id, title, state, priority, due FROM task WHERE project_id = 1 + :r % 1000 AND state = 'open' ORDER BY priority DESC, due LIMIT 20;
SELECT t.id, t.title, m.email, p.name FROM task t JOIN project p ON p.id = t.project_id LEFT JOIN member m ON m.id = t.assignee
  WHERE p.tenant_id = 1 + :q % 50 AND t.state IN ('open', 'doing') AND (t.due IS NULL OR t.due < '2026-01-01') ORDER BY t.due NULLS LAST, t.priority DESC LIMIT 25 OFFSET 5;
SELECT l.name, count(*) FROM task_label tl JOIN label l ON l.id = tl.label_id JOIN task t ON t.id = tl.task_id
  WHERE t.project_id = 1 + :r % 1000 GROUP BY l.id HAVING count(*) > 0 ORDER BY 2 DESC, 1;
SELECT c.body, c.at, m.email FROM comment c LEFT JOIN member m ON m.id = c.author WHERE c.task_id = 1 + :q % 20000 ORDER BY c.at DESC LIMIT 5;
SELECT * FROM open_task WHERE tenant_id = 1 + :r % 50 AND assignee = 1 + :q % 1000;
SELECT email, open, hours FROM member_load WHERE id = 1 + :r % 1000;
SELECT state, count(*) AS n, avg(estimate), sum(priority > 3) FROM task WHERE project_id IN (SELECT id FROM project WHERE tenant_id = 1 + :q % 50) GROUP BY state ORDER BY n DESC;
SELECT id FROM task WHERE lower(title) LIKE lower(substr(:t, 1, 4)) || '%' LIMIT 10;
SELECT m.id, m.email, (SELECT count(*) FROM comment c WHERE c.author = m.id) AS comments,
    EXISTS (SELECT 1 FROM session s WHERE s.member_id = m.id AND s.expires > :q) AS online
  FROM member m WHERE m.tenant_id = 1 + :r % 50 AND m.email LIKE '%@example.test' ORDER BY comments DESC, m.email LIMIT 10;
WITH recent AS (SELECT task_id, max(at) AS last FROM comment WHERE at > 1750000000 + :r % 20000 GROUP BY task_id)
  SELECT t.id, t.title, r.last FROM recent r JOIN task t ON t.id = r.task_id WHERE t.state != 'done' ORDER BY r.last DESC LIMIT 10;
UPDATE task SET state = 'doing', priority = min(5, priority + 1) WHERE id = 1 + :r % 20000 AND state = 'open';
UPDATE task SET state = 'done' WHERE id = 1 + :q % 20000;
INSERT INTO comment(task_id, author, body, at) VALUES (1 + :r % 20000, 1 + :q % 1000, :t, 1760000000 + :i);
DELETE FROM session WHERE expires < :q % 100000000;
INSERT INTO session(token, member_id, expires, data) VALUES (:b, 1 + :r % 1000, :q, :t) ON CONFLICT(token) DO UPDATE SET expires = excluded.expires;
SELECT value FROM setting WHERE scope = 'tenant:' || (:r % 50) AND key = :s;
INSERT INTO setting(scope, key, value) VALUES ('user:' || (:q % 1000), 'theme', :s) ON CONFLICT DO UPDATE SET value = excluded.value WHERE value != excluded.value;
SELECT count(*) FROM task t WHERE NOT EXISTS (SELECT 1 FROM task_label tl WHERE tl.task_id = t.id) AND t.project_id = 1 + :r % 1000;
SELECT p.name, sum(t.state = 'done') * 100.0 / count(*) AS pct FROM project p JOIN task t ON t.project_id = p.id WHERE p.tenant_id = 1 + :r % 50 GROUP BY p.id ORDER BY pct DESC LIMIT 5;
@@prepare 40
PRAGMA table_info(task);
PRAGMA table_xinfo(member);
PRAGMA index_list(task);
PRAGMA index_info(task_project_state);
PRAGMA index_xinfo(member_tenant);
PRAGMA foreign_key_list(task);
PRAGMA table_list;
SELECT count(*), max(name) FROM pragma_database_list;
PRAGMA user_version;
PRAGMA application_id;
PRAGMA journal_mode;
PRAGMA page_size;
PRAGMA page_count;
PRAGMA encoding;
PRAGMA collation_list;
PRAGMA compile_options;
PRAGMA wal_autocheckpoint;
PRAGMA synchronous;
PRAGMA cache_size;
PRAGMA foreign_keys;
SELECT type, name, tbl_name, sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name;
SELECT name FROM pragma_table_info('task') WHERE "notnull" ORDER BY cid;
SELECT m.name, i.name FROM sqlite_schema m, pragma_index_list(m.name) i WHERE m.type = 'table' ORDER BY 1, 2;
SELECT count(*) FROM pragma_function_list;
EXPLAIN QUERY PLAN SELECT * FROM open_task WHERE tenant_id = 3 ORDER BY due;
@@run
UPDATE project SET archived = 1 WHERE id % 40 = 0;
DELETE FROM tenant WHERE id > 45;
@@open 150
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;
SELECT id, title FROM task WHERE project_id = 1 + :r % 1000 AND state = 'open' ORDER BY priority DESC LIMIT 5;
UPDATE task SET estimate = estimate + 0.25 WHERE id = 1 + :q % 20000;
@@run
CREATE TABLE note2(id INTEGER PRIMARY KEY, body TEXT);
ALTER TABLE task ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;
CREATE INDEX task_archived ON task(archived, project_id);
DROP INDEX task_title;
DROP VIEW member_load;
DROP TRIGGER member_check;
DROP TABLE note2;
ANALYZE;
PRAGMA optimize;
PRAGMA foreign_key_check;
PRAGMA integrity_check;
PRAGMA wal_checkpoint(RESTART);
SELECT count(*), sum(priority), sum(state = 'done') FROM task;
SELECT count(*) FROM comment;
SELECT count(*) FROM task_label;
SELECT count(*) FROM session;
SELECT count(*) FROM member;
