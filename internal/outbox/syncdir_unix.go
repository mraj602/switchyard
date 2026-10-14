//go:build unix

package outbox

import "os"

// syncDir makes a new, renamed or removed name in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
