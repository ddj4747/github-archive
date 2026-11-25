package githubarchive

import (
	_ "gopkg.in/yaml.v3"
	"modernc.org/sqlite"
)

// These imports keep gopkg.in/yaml.v3 and modernc.org/sqlite in go.mod
// before the service packages exist.
// The archive opener must use a plain filename or one SQL statement per _pragma value, and must call sqlite.StrictPragmas(true) immediately before Open because init order does not cover packages that initialize earlier.
func init() {
	sqlite.StrictPragmas(true)
}
