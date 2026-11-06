// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

//go:build ((linux || darwin) && (amd64 || arm64)) || (windows && amd64)

// Package sqlite is a SQLite driver for database/sql that needs no cgo.
//
// It registers itself as "sqlite":
//
//	db, err := sql.Open("sqlite", "file:app.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
//
// modernc.org/sqlite and github.com/glebarez/go-sqlite register the same
// name, so a program that imports one of them as well panics at start. Use
// sql.OpenDB(sqlite.NewConnector(dsn)) to open a database without the
// registered name.
//
// # The data source name
//
// A file name, ":memory:", or a URI ("file:app.db?mode=ro"), which SQLite
// reads itself (https://sqlite.org/uri.html). These parameters are the
// driver's and SQLite does not see them:
//
//   - _pragma=name(value): a PRAGMA run on every new connection, in order.
//     It may be given more than once.
//   - _txlock=deferred, immediate or exclusive: how BEGIN starts a
//     transaction (deferred by default). A read-only transaction always
//     starts deferred.
//   - _time_format=sqlite: how a time.Time is written. The only format, and
//     the default, is "2006-01-02 15:04:05.999999999-07:00".
//
// A connection waits 5 seconds for a lock unless a _pragma sets busy_timeout.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/liustack/sqlite/internal/csqlite"
)

func init() {
	sql.Register("sqlite", &Driver{})
}

// Driver is the database/sql driver.
type Driver struct{}

// Open opens a connection to the database named by dsn.
func (d *Driver) Open(dsn string) (driver.Conn, error) {
	c, err := d.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return c.Connect(context.Background())
}

// OpenConnector parses dsn once for every connection of a pool.
func (d *Driver) OpenConnector(dsn string) (driver.Connector, error) {
	return NewConnector(dsn)
}

// Connector opens connections to one database.
type Connector struct {
	name    string // what SQLite opens
	pragmas []string
	begin   string
	opts    options // what Register and NewConnector gave
	// key is the same for connectors that open a file the same way: group
	// commit merges statements only between connections of one key.
	key string
}

// NewConnector parses dsn, for sql.OpenDB. Its connections get what
// Register gave, then opts.
func NewConnector(dsn string, opts ...Option) (*Connector, error) {
	c := &Connector{begin: "BEGIN"}
	o, setKey, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	c.opts = o
	name, query, _ := strings.Cut(dsn, "?")
	q, err := url.ParseQuery(query)
	if err != nil {
		return nil, fmt.Errorf("sqlite: data source name %q: %w", dsn, err)
	}
	for _, p := range q["_pragma"] {
		c.pragmas = append(c.pragmas, "PRAGMA "+p)
	}
	switch l := q.Get("_txlock"); l {
	case "", "deferred":
	case "immediate", "exclusive":
		c.begin = "BEGIN " + strings.ToUpper(l)
	default:
		return nil, fmt.Errorf("sqlite: _txlock=%s: want deferred, immediate or exclusive", l)
	}
	switch f := q.Get("_time_format"); f {
	case "", "sqlite":
	default:
		return nil, fmt.Errorf("sqlite: _time_format=%s: the only format is sqlite", f)
	}
	if q.Get("cache") == "shared" {
		// SQLite is built without shared cache (engine/sqlite.c), and would
		// open each connection its own database without a word.
		return nil, errors.New("sqlite: cache=shared is not built in: use file:/name?vfs=memdb for a memory database that connections share")
	}
	for k := range q {
		switch {
		case k == "_pragma", k == "_txlock", k == "_time_format":
			q.Del(k)
		case strings.HasPrefix(k, "_"):
			return nil, fmt.Errorf("sqlite: unknown parameter %s: the driver takes _pragma, _txlock and _time_format (for example _pragma=foreign_keys(1))", k)
		}
	}
	if strings.HasPrefix(name, "file:") {
		if len(q) > 0 {
			name += "?" + q.Encode()
		}
	} else if len(q) > 0 {
		return nil, fmt.Errorf("sqlite: data source name %q: SQLite reads parameters only from a file: URI", dsn)
	}
	c.name = name
	c.key = name + "\x00" + strings.Join(c.pragmas, "\x00") + "\x00" + setKey
	return c, nil
}

// Connect opens a connection.
func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	cc, err := csqlite.Open(c.name, csqlite.OpenReadWrite|csqlite.OpenCreate|csqlite.OpenURI|csqlite.OpenNoMutex, "")
	if err != nil {
		return nil, err
	}
	conn := &conn{c: cc, begin: c.begin, key: c.key}
	if err := c.setUp(conn); err != nil {
		cc.Close()
		if conn.wl != nil {
			closeWriteLock(conn.wl)
		}
		return nil, err
	}
	return conn, nil
}

// setUp makes conn, just opened, what the connector says.
func (c *Connector) setUp(conn *conn) error {
	cc := conn.c
	var attached bool
	if path := cc.Filename("main"); path != "" {
		var err error
		if conn.wl, attached, err = openWriteLock(path); err != nil {
			return err
		}
	}
	if attached {
		// The replicator runs every checkpoint, after it has read the
		// frames (replhook.Attach), also against the PRAGMAs of the
		// connector.
		if err := cc.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
			return err
		}
		cc.SetGuard(true)
	}
	if err := c.opts.setUp(cc); err != nil {
		return err
	}
	if err := cc.BusyTimeout(5000); err != nil {
		return err
	}
	for _, p := range c.pragmas {
		if err := cc.Exec(p); err != nil {
			return fmt.Errorf("sqlite: %s: %w", p, err)
		}
	}
	ms, err := pragmaInt(cc, "busy_timeout")
	if err != nil {
		return err
	}
	conn.busyTimeout = time.Duration(ms) * time.Millisecond
	// A connection SQLite opened read-only, as when the file is, runs
	// differently from the others of its key. So does one the DSN gave
	// defer_foreign_keys, which SQLite turns off at the end of each
	// transaction: connections that had one and connections that had none
	// differ.
	conn.alone = cc.ReadOnly()
	if len(c.pragmas) > 0 && !conn.alone {
		dfk, err := pragmaInt(cc, "defer_foreign_keys")
		if err != nil {
			return err
		}
		conn.alone = dfk != 0
	}
	return nil
}

// pragmaInt reads the value of a PRAGMA that is a number.
func pragmaInt(c *csqlite.Conn, name string) (int64, error) {
	s, _, err := c.Prepare("PRAGMA " + name)
	if err != nil {
		return 0, err
	}
	defer s.Finalize()
	row, err := s.Step()
	if err != nil || !row {
		return 0, err
	}
	return s.ColumnInt64(0), nil
}

// Driver is the driver of the connector.
func (c *Connector) Driver() driver.Driver { return &Driver{} }
