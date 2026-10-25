package main

import "time"

// storeSweepInterval is how often a retention sweep runs: far shorter than any
// retention it enforces, and far longer than a request.
const storeSweepInterval = time.Minute
