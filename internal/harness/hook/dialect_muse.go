package hook

// monitor is Muse Code's dialect (config.DialectMonitor; card 88): the
// documents are Claude Code's in shape, and what the hook prints is capped
// in bytes as well as in characters. The rest is the plain form. The line
// that tells the model to arm its monitor is no part of it: that line
// says how a message arrives, and goes by the host's delivery.
type monitor struct{ plugin }

func (monitor) byteCap() int { return museByteCap }
