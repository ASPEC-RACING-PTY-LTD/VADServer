module github.com/ASPEC-RACING-PTY-LTD/vad-server

go 1.26.0

require (
	github.com/go-chi/chi/v5 v5.5.0
	github.com/oschwald/maxminddb-golang/v2 v2.8.0
	golang.org/x/crypto v0.58.0
	golang.org/x/sys v0.49.0
	golang.org/x/time v0.16.0
	modernc.org/sqlite v1.60.1
)

require github.com/klauspost/compress v1.20.3 // indirect

require (
	github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk v0.0.0-00010101000000-000000000000
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// Temporary until sdk/v0.1.0 is published: build against the sibling VaultDock
// checkout. Remove this replace and require the tagged version afterwards.
replace github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk => ./sdk
