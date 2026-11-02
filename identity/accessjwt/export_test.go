package accessjwt

import "net/http"
import r "idp.local/reporting/reporting"

// Pinned is the configuration in force at the moment Pin was called: the snapshot an authentication that has already started is judged by.
type Pinned struct{ c *configuration }

// Pin takes the snapshot a started authentication would hold.
func (v *Verifier) Pin() Pinned { return Pinned{v.current.Load()} }

// Authenticate runs the rest of an authentication against the pinned snapshot, whatever Replace has done since.
func (p Pinned) Authenticate(req *http.Request) (r.Principal, error) { return p.c.authenticate(req) }
