// Reference-only salvage. This nested module isolates the pi-server reference
// sources from the parent Yuno module so `go build ./...` does not try to
// compile read-only donor code (which imports the original private module).
// It is never built — it exists to be read per the PORT.md map.
module github.com/giantbeaver9/pi-home-server-page/backend

go 1.24
