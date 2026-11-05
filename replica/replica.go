// Copyright (c) 2026 The liustack/sqlite Authors.
// SPDX-License-Identifier: MIT
// Use of this source code is governed by the MIT license in LICENSE at the
// root of this repository.

// Package replica copies a database of the driver to S3 (AWS S3, Cloudflare
// R2, MinIO and the other stores that speak its API) as it changes, so that
// it can be restored after the machine is lost.
//
// Start it before the program opens the database, then open the database
// as usual:
//
//	r, err := replica.New(ctx, "app.db", replica.Config{
//		URL: "s3://bucket/app.db?endpoint=https://<account>.r2.cloudflarestorage.com&region=auto",
//	})
//	if err != nil {
//		return err
//	}
//	defer r.Close()
//	db, err := sql.Open("sqlite", "app.db")
//
// Each commit is copied into a local staging folder beside the database
// within SyncInterval and uploaded from there. Writes never wait for S3.
// The replica is laid out as Litestream lays it out, so `litestream
// restore` reads it too.
package replica

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/superfly/ltx"

	"github.com/liustack/sqlite/replica/internal/capture"
	"github.com/liustack/sqlite/replica/internal/s3"
)

// Credentials are the keys requests are signed with. Printing them never
// shows the secret or the session token.
type Credentials = s3.Credentials

// A CredentialsProvider gives the credentials of each request, such as the
// AWS SDK's provider of an instance role, wrapped.
type CredentialsProvider = s3.CredentialsProvider

// StaticCredentials gives the same credentials to every request.
func StaticCredentials(c Credentials) CredentialsProvider { return s3.Static(c) }

// Config says where the replica goes and how.
type Config struct {
	// URL is s3://bucket/path. Query parameters: endpoint, the scheme and
	// host of the store (default https://s3.<region>.amazonaws.com), and
	// region (default AWS_REGION, else us-east-1). R2 takes region=auto.
	URL string
	// Credentials sign the requests. nil reads AWS_ACCESS_KEY_ID,
	// AWS_SECRET_ACCESS_KEY and AWS_SESSION_TOKEN.
	Credentials CredentialsProvider
	// SyncInterval is the most time between a commit and its copy into
	// the staging folder. Default 1s.
	SyncInterval time.Duration
	// AllowHTTP takes an http:// endpoint, such as a local MinIO.
	AllowHTTP bool
	// SingleWriter says the program makes sure only one replicator writes
	// to the replica, so New does not check that the store refuses a second
	// write of a key. Without it, a store that does not is refused.
	SingleWriter bool
	// FreshStart lets New go on when the replica does not end where the
	// staged files do (another machine wrote it, or the database was
	// replaced): it uploads a full copy of the database as the replica's
	// next TXID. Without it New fails with ErrDiverged.
	FreshStart bool
	// Retention is how long the replica keeps the database's past: a
	// restore goes back that far at least. A snapshot of the database is
	// uploaded every Retention or every day, the more often. Default 24h.
	Retention time.Duration
	// StagingLimit is the most bytes of changes staged and not uploaded
	// yet, such as while the store is down. Past it, staging stops (an error
	// under ErrStagingFull) and keeps what it has: checkpoints go on, so the
	// WAL stays bounded, and writes never wait. Once what is staged is
	// uploaded, a full copy of the database follows it. The changes in
	// between have no point of their own to restore to. The last full
	// copy, which staging goes on from, is not counted: the staging folder
	// may hold one database more. Default 1 GiB.
	StagingLimit int64

	schedule func(*schedule) // set by tests: changes the schedule
	// OnError, if set, gets the errors of the work done in the background.
	// It is called with no lock of the replica held, from New, Close or a
	// goroutine of the replica, and should return soon, as a log does. It
	// must not call Close, which waits for those goroutines, nor wait for a
	// write to the database: the caller of Close may hold the write lock.
	OnError func(error)
}

// ErrStagingFull is under the error given to Config.OnError when staging
// stops at Config.StagingLimit.
var ErrStagingFull = capture.ErrStagingFull

// ErrDiverged is under the error of New when the replica does not end
// where the staged files do. Config.FreshStart goes on from the replica.
var ErrDiverged = errors.New("replica: the replica and the database went apart")

// Status is where replication is.
type Status struct {
	Staged   uint64 // the last TXID in the staging folder
	Uploaded uint64 // the last TXID in the replica
	Pending  int64  // the bytes staged and not uploaded yet
	// Err is the error of the last upload, nil once one succeeds. It is
	// under ErrConflict when uploads stopped for good.
	Err error
}

// A Replica copies one database file to S3.
type Replica struct {
	cfg    Config
	client *s3.Client
	path   string // of the replica in the bucket
	c      *capture.Capturer

	cancel context.CancelFunc
	done   chan struct{} // closed when run returns

	// busy holds a value while the replicator works on the replica: one
	// upload, compaction or deletion at a time.
	busy  chan struct{}
	files []remoteFile // the files of the replica, in key order, under busy
	sched schedule
	// A snapshot taken and not uploaded yet, under busy.
	snap     string
	snapTXID ltx.TXID

	mu     sync.Mutex
	status Status
}

// closeUpload is how long Close tries to upload what is left.
const closeUpload = 10 * time.Second

// New starts replicating the database file name, which the driver of this
// process must not have open yet. It checks that the store refuses a
// second write of a key (unless Config.SingleWriter), reads where the
// replica ends, and goes on from there when the staged files do, else fails
// with ErrDiverged (unless Config.FreshStart). An empty replica starts with
// a full copy. ctx bounds the requests New makes.
func New(ctx context.Context, name string, cfg Config) (*Replica, error) {
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = time.Second
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 24 * time.Hour
	}
	if cfg.StagingLimit <= 0 {
		cfg.StagingLimit = 1 << 30
	}
	client, path, err := cfg.client()
	if err != nil {
		return nil, err
	}
	if !cfg.SingleWriter {
		if err := probe(ctx, client, path); err != nil {
			return nil, err
		}
	}
	files, err := listRemote(ctx, client, path)
	if err != nil {
		return nil, err
	}
	end, err := readEnd(ctx, client, files)
	if err != nil {
		return nil, err
	}
	r := &Replica{cfg: cfg, client: client, path: path, files: files, sched: defaultSchedule(cfg.Retention), done: make(chan struct{}), busy: make(chan struct{}, 1)}
	if cfg.schedule != nil {
		cfg.schedule(&r.sched)
	}
	r.status.Uploaded = uint64(end.txid)
	r.c, err = capture.Open(name, capture.Options{
		Interval: cfg.SyncInterval,
		Limit:    cfg.StagingLimit,
		Uploaded: end.txid,
		OnError:  r.report,
		Start: func(staged []capture.StagedFile) (*capture.Restart, error) {
			return startFrom(end, staged, cfg.FreshStart)
		},
	})
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.run(runCtx)
	return r, nil
}

// startFrom decides how the staged files go on from the replica's end.
func startFrom(end remoteEnd, staged []capture.StagedFile, fresh bool) (*capture.Restart, error) {
	if end.txid == 0 {
		// Staged files that do not start at TXID 1 were for another
		// replica: start over, nothing can go apart.
		if len(staged) > 0 && staged[0].TXID != 1 {
			return &capture.Restart{}, nil
		}
		return nil, nil
	}
	for _, f := range staged {
		if f.TXID == end.txid && f.Post == end.post || f.Min == end.txid+1 && f.Pre == end.post {
			return nil, nil
		}
	}
	if fresh {
		return &capture.Restart{TXID: end.txid, Post: end.post}, nil
	}
	if len(staged) == 0 {
		return nil, fmt.Errorf("%w: the replica ends at TXID %s and nothing is staged", ErrDiverged, end.txid)
	}
	return nil, fmt.Errorf("%w: the replica ends at TXID %s, the staged files are TXIDs %s to %s",
		ErrDiverged, end.txid, staged[0].TXID, staged[len(staged)-1].TXID)
}

func (r *Replica) report(err error) {
	if r.cfg.OnError != nil {
		r.cfg.OnError(err)
	}
}

func (r *Replica) uploaded() ltx.TXID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ltx.TXID(r.status.Uploaded)
}

func (r *Replica) conflict() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if errors.Is(r.status.Err, ErrConflict) {
		return r.status.Err
	}
	return nil
}

func (r *Replica) setStatus(f func(*Status)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(&r.status)
}

// Status is where replication is now.
func (r *Replica) Status() Status {
	r.mu.Lock()
	s := r.status
	r.mu.Unlock()
	s.Staged = uint64(r.c.Durable())
	s.Pending = r.c.Pending()
	return s
}

// Sync stages what was committed up to now and uploads it. When staging
// stopped at StagingLimit, it uploads what is staged first, so that staging
// goes on.
func (r *Replica) Sync(ctx context.Context) error {
	err := r.c.Sync(ctx)
	if errors.Is(err, ErrStagingFull) {
		if err := r.upload(ctx); err != nil {
			return err
		}
		err = r.c.Sync(ctx)
	}
	if err != nil {
		return err
	}
	return r.upload(ctx)
}

// Close stages what is left, checkpoints, and uploads what is staged,
// trying for 10 seconds, waiting for a Sync that uploads too. What it could
// not upload stays staged, for the next New.
func (r *Replica) Close() error {
	r.cancel()
	<-r.done
	err := r.c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), closeUpload)
	defer cancel()
	return errors.Join(err, r.upload(ctx))
}

// client is the client of the store of cfg.URL and the path of the replica
// in the bucket.
func (cfg Config) client() (*s3.Client, string, error) {
	scfg, path, err := parseURL(cfg.URL)
	if err != nil {
		return nil, "", err
	}
	scfg.Credentials, scfg.AllowHTTP = cfg.Credentials, cfg.AllowHTTP
	if scfg.Credentials == nil {
		scfg.Credentials = s3.EnvCredentials{}
	}
	client, err := s3.New(scfg)
	return client, path, err
}
