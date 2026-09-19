package termview

import "io"

type Option func(*Model)

func WithCommand(cmd string, args ...string) Option {
	return func(tw *Model) {
		tw.command = cmd
		tw.commandArgs = append([]string(nil), args...)
	}
}

func WithInitialWidth(width int) Option {
	return func(tw *Model) {
		tw.width = width
	}
}

func WithInitialHeight(height int) Option {
	return func(tw *Model) {
		tw.height = height
	}
}

func WithScrollbackSize(lines int) Option {
	return func(tw *Model) {
		tw.scrollbackSize = lines
	}
}

func WithStdErr(stdErr io.Writer) Option {
	return func(tw *Model) {
		tw.stdErr = stdErr
	}
}
