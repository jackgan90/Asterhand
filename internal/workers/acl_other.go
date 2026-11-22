//go:build !windows

package workers

func checkDeepSeekWorkspaceACL(workspaceRoot string) []string { return nil }
