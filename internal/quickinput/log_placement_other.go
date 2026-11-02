//go:build !windows

package quickinput

import "github.com/jackgan90/Asterhand/internal/quickinput/hostipc"

func defaultLogAnchor() *hostipc.WindowAnchor { return nil }
