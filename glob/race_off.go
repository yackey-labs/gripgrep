//go:build !race

package glob

// raceDetectorEnabled is false in a normal (non -race) build. See
// race_on.go.
const raceDetectorEnabled = false
