package commands

import (
	"context"
	"flag"

	"james/hem/pkg/protocol"
)

func (e *Executor) ResolveAttention(args []string) *protocol.Response {
	var revision int64 = -1
	remaining, err := parseFlagsFromArgs("resolve-attention", args, func(fs *flag.FlagSet) {
		fs.Int64Var(&revision, "revision", -1, "current attention revision")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if len(remaining) != 1 || revision < 0 {
		return protocol.ErrResponse("resolve attention SESSION_ID --revision N is required")
	}
	mp, err := e.resolveSessionMoneypenny(remaining[0])
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	resp, err := e.sendCommand(context.Background(), mp, "update_attention", map[string]interface{}{
		"session_id": remaining[0], "kind": "", "revision": revision,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	e.invalidateMPCache(mp.Name)
	return &protocol.Response{Status: protocol.StatusOK, Data: resp.Data}
}
