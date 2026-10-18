// Package testkit exposes the in-process test server to other modules, such as
// the desktop client's end-to-end tests, which cannot import internal packages.
package testkit

import "github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/testserver"

// Server is an in-process VaultDock server with an admin session and helpers.
type Server = testserver.Server

// Start runs a fresh server for the duration of the test.
var Start = testserver.Start

// Password is the admin password of every test server.
const Password = testserver.Password
