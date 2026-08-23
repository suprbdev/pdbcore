// Command fixture prints testutil.FixtureSQL so product repos can generate
// their compose init script from the module:
//
//	go run github.com/suprbdev/pdbcore/testutil/cmd/fixture > db/init/01-schema.sql
package main

import (
	"fmt"
	"os"

	"github.com/suprbdev/pdbcore/testutil"
)

func main() {
	if _, err := fmt.Fprint(os.Stdout, testutil.FixtureSQL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
