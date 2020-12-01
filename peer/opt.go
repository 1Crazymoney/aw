package peer

import (
	"github.com/renproject/aw/wire"
	"github.com/renproject/id"
	"go.uber.org/zap"
)

type Options struct {
	Logger   *zap.Logger
	PrivKey  *id.PrivKey
	Receiver Receiver
}

func DefaultOptions() Options {
	logger, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	privKey := id.NewPrivKey()
	return Options{
		Logger:  logger,
		PrivKey: privKey,
		Receiver: Callbacks{
			OnDidReceiveMessage: func(id.Signatory, wire.Msg) {},
		},
	}
}

func (opts Options) WithLogger(logger *zap.Logger) Options {
	opts.Logger = logger
	return opts
}

func (opts Options) WithPrivKey(privKey *id.PrivKey) Options {
	opts.PrivKey = privKey
	return opts
}

func (opts Options) WithReceiver(receiver Receiver) Options {
	opts.Receiver = receiver
	return opts
}
