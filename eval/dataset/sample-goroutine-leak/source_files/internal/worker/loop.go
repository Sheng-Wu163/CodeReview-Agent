package worker

import "time"

// Start launches the periodic job.
func Start(fn func()) {
	go func() {
		for {
			fn()
			time.Sleep(time.Minute)
		}
	}()
}