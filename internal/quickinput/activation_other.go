//go:build !windows

package quickinput

type immediateActivation struct{}

func (immediateActivation) valid() bool   { return true }
func captureActivation() activationIntent { return immediateActivation{} }
