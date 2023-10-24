package utils

func Defer(stopChan <-chan struct{}, fn func()) {
	go func() {
		<-stopChan
		fn()
	}()
}
