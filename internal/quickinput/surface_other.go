//go:build !windows

package quickinput

func NativeFactory(*Controller) SurfaceFactory { return nil }
