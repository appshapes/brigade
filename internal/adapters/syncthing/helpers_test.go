package syncthing

import (
	"log/slog"

	"github.com/appshapes/brigade/internal/adapterkit"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func configDir(environ []string) (string, error) { return adapterkit.ConfigDir(environ) }
