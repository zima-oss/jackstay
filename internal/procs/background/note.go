package background

import "github.com/zima-oss/jackstay/internal/procs"

// note records a group's event on the running command's span (procs.Note); the tests of this
// package replace it to see the events of an in-process run.
var note = procs.Note
