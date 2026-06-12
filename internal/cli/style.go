package cli

import (
	"fmt"
	"io"
	"os"
)

type style struct {
	color bool
}

func newStyle(_ io.Writer, noColor bool) style {
	if noColor {
		return style{color: false}
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return style{color: false}
	}
	return style{color: info.Mode()&os.ModeCharDevice != 0}
}

func (s style) ok(text string) string {
	return s.paint("32", text)
}

func (s style) warn(text string) string {
	return s.paint("33", text)
}

func (s style) clean(text string) string {
	return s.paint("36", text)
}

func (s style) paint(code, text string) string {
	if !s.color {
		return text
	}
	return fmt.Sprintf("\x1b[%sm%s\x1b[0m", code, text)
}
