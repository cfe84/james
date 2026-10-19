package commands

import (
	"context"
	"time"

	"james/hem/pkg/protocol"
	"james/moneypenny/pkg/updater"
)

// MoneypennySetup supplies authenticated archive metadata for target-side setup.
func (e *Executor) MoneypennySetup(args []string) *protocol.Response {
	if len(args) != 0 {
		return protocol.ErrResponse("usage: hem setup moneypenny")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	version, artifacts, err := updater.BootstrapArtifacts(ctx)
	if err != nil {
		return protocol.ErrResponse("loading verified setup release: " + err.Error())
	}
	return protocol.OKResponse(map[string]interface{}{"version": version, "artifacts": artifacts})
}
