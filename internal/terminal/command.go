package terminal

import (
	"github.com/aymanbagabas/go-pty"
	"lightremote/internal/executil"
)

func startPTYCommand(cmd *pty.Cmd) error {
	if err := executil.PreparePTY(cmd); err != nil {
		return err
	}
	return cmd.Start()
}
