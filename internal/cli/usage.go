package cli

import (
	"io"
	"strings"
)

// gutter is the column the one-line summaries start in. A name longer than
// the gutter puts its summary on the next line rather than pushing the
// whole column out.
const gutter = 46

// usageAll renders the top-level usage block. The command table is split in
// two so the block cannot overstate what this binary does: the first list
// works, the second names the plan task that will build it. Hidden
// commands appear only for `brigade help --all`.
func usageAll(all bool) string {
	var b strings.Builder
	b.WriteString("Usage: " + Program + " <command> [arguments]\n")
	b.WriteString("\nTeam messaging between the Claude Code sessions of different people.\n")

	var ready, planned []Command
	for _, c := range commands {
		if c.Hidden && !all {
			continue
		}
		if c.Implemented() {
			ready = append(ready, c)
		} else {
			planned = append(planned, c)
		}
	}

	if len(ready) > 0 {
		b.WriteString("\nCommands:\n")
		for _, c := range ready {
			b.WriteString(commandLine(c))
		}
	}
	if len(planned) > 0 {
		b.WriteString("\nNot implemented yet (the plan task that builds each is in brackets):\n")
		for _, c := range planned {
			b.WriteString(commandLine(c))
		}
	}

	b.WriteString("\nGlobal flags:\n")
	b.WriteString(pad("  --json") + "write the protocol JSON envelope to stdout instead of human output\n")
	b.WriteString(pad("  --log-level "+strings.Join(LogLevels, "|")) + "log verbosity\n")

	b.WriteString("\n")
	if !all {
		b.WriteString("Run `" + Program + " help --all` to include the hook, watcher and adapter entrypoints.\n")
	}
	b.WriteString("Run `" + Program + " help <command>` for one command.\n")
	return b.String()
}

// pad right-pads s to the summary gutter, or breaks the line when s is
// already at or past it.
func pad(s string) string {
	if n := len(s); n < gutter {
		return s + strings.Repeat(" ", gutter-n)
	}
	return s + "\n" + strings.Repeat(" ", gutter)
}

// commandLine renders one row of a command list.
func commandLine(c Command) string {
	line := pad("  "+signature(c)) + c.Summary
	if !c.Implemented() {
		line += "  [" + c.Task + "]"
	}
	return line + "\n"
}

// signature is the command's name followed by its argv summary.
func signature(c Command) string {
	if c.Args == "" {
		return c.Name
	}
	return c.Name + " " + c.Args
}

// usageCommand renders the usage block for one command.
func usageCommand(c Command) string {
	var b strings.Builder
	b.WriteString("Usage: " + Program + " " + signature(c) + " [--json] [--log-level " + strings.Join(LogLevels, "|") + "]\n")
	b.WriteString("\n" + c.Summary + "\n")
	if !c.Implemented() {
		b.WriteString("\nNot implemented yet; it arrives with plan task " + c.Task + ".\n")
	}
	if c.Hidden {
		b.WriteString("\nThis entrypoint is for hooks, the watcher, adapters and tests, not for humans.\n")
	}
	return b.String()
}

// writeUsage sends a usage block to the stream a caller of this kind
// expects: stdout for a human (7.3), stderr for a machine caller, because
// stdout carries protocol output only and usage is not protocol output.
// writeUsage emits help. In human mode the usage block is the output and goes
// to stdout (7.3). In --json mode stdout must carry exactly one JSON document,
// so the usage text travels inside a success envelope rather than being dumped
// to stderr and leaving stdout empty -- a machine caller asking for help must
// be able to tell success from a silent failure.
func writeUsage(s Streams, jsonMode bool, text string) {
	if jsonMode {
		if err := WriteResult(s.Out, usageResult{Usage: text}); err != nil {
			_, _ = io.WriteString(s.Err, text)
		}
		return
	}
	_, _ = io.WriteString(s.Out, text)
}

// usageResult is the --json shape of a help request.
type usageResult struct {
	Usage string `json:"usage"`
}
