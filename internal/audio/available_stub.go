//go:build !cgo

package audio

func Available() Backend { return nil }
