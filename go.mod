module github.com/liustack/sqlite

go 1.26.6

require (
	github.com/superfly/ltx v0.5.2
	golang.org/x/arch v0.32.0
	golang.org/x/sys v0.49.0
)

require github.com/pierrec/lz4/v4 v4.1.23 // indirect

// A writer beside readers that keep every core busy is starved, and a pool
// of connections reads slower beside writes. Both are fixed in v0.1.7.
retract v0.1.6
