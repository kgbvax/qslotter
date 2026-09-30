//go:build !windows

package tray

// AcquireSingleInstance is a no-op off Windows (the single-instance guard is
// only needed where the tray and UDP/SQLite contention matter).
func AcquireSingleInstance() (alreadyRunning bool, err error) {
	return false, nil
}
