package main

import (
	"context"
	"encoding/json"

	"braces.dev/errtrace"
	"github.com/coryb/llblib"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/gateway/grpcclient"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/bklog"
)

func main() {
	if err := grpcclient.RunFromEnvironment(
		appcontext.Context(), build,
	); err != nil {
		bklog.L.Errorf("fatal error: %+v", err)
		panic(err)
	}
}

func build(ctx context.Context, c client.Client) (_ *client.Result, retErr error) {
	// this custom frontend just returns a ref to the image argument, but can
	// return any marshalled state.
	imageName := c.BuildOpts().Opts["custom:load-image"]
	if imageName == "" {
		return nil, errtrace.New("custom:load-image option is required")
	}

	st := llblib.Image(imageName)

	def, err := st.Marshal(ctx)
	if err != nil {
		return nil, errtrace.Errorf("failed to marshal state for %s: %w", imageName, err)
	}

	res, err := c.Solve(ctx, client.SolveRequest{
		Definition: def.ToPB(),
	})
	if err != nil {
		return nil, errtrace.Errorf("failed to solve state for %s: %w", imageName, err)
	}

	if config, err := llblib.LoadImageConfig(ctx, st); err != nil {
		return nil, errtrace.Errorf("failed to load image config for %s: %w", imageName, err)
	} else if config != nil {
		encodedConfig, err := json.Marshal(config)
		if err != nil {
			return nil, errtrace.Errorf("failed to marshal image config for %s: %w", imageName, err)
		}
		res.AddMeta(exptypes.ExporterImageConfigKey, encodedConfig)
	}

	return res, err
}
