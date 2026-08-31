package main

import (
	"context"
	"encoding/json"
)

// PostgrestError is PostgREST's error body: {"code","message","details","hint"} (all strings or null).
type PostgrestError struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Details *string `json:"details"`
	Hint    *string `json:"hint"`
}

// rpc: POST /rest/v1/rpc/<fn> with apikey + Authorization: Bearer <user JWT> and the brigade schema headers.
// schemaMode: "both" (Accept-Profile + Content-Profile), "content", "accept", "none".
func rpc(ctx context.Context, env Env, apikey, jwt, fn string, args any, schemaMode, label string) Resp {
	h := map[string]string{
		"apikey":       apikey,
		"Content-Type": "application/json",
	}
	if jwt != "" {
		h["Authorization"] = "Bearer " + jwt
	}
	switch schemaMode {
	case "both":
		h["Accept-Profile"] = "brigade"
		h["Content-Profile"] = "brigade"
	case "content":
		h["Content-Profile"] = "brigade"
	case "accept":
		h["Accept-Profile"] = "brigade"
	}
	if args == nil {
		args = map[string]any{}
	}
	return do(ctx, label, "POST", env.APIURL+"/rest/v1/rpc/"+fn, h, args)
}

func rpcQuiet(ctx context.Context, env Env, apikey, jwt, fn string, args any) Resp {
	h := map[string]string{
		"apikey":          apikey,
		"Content-Type":    "application/json",
		"Authorization":   "Bearer " + jwt,
		"Accept-Profile":  "brigade",
		"Content-Profile": "brigade",
	}
	if args == nil {
		args = map[string]any{}
	}
	return doQuiet(ctx, "POST", env.APIURL+"/rest/v1/rpc/"+fn, h, args)
}

func parsePgError(b []byte) (PostgrestError, bool) {
	var e PostgrestError
	if err := json.Unmarshal(b, &e); err != nil || e.Code == "" {
		return e, false
	}
	return e, true
}
