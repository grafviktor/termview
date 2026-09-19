# Termview - TUI terminal component for Go #

Termview is a terminal component which is designed to work with [Bubble Tea](https://github.com/charmbracelet/bubbletea) v2. It embeds a real shell session in your app using the same model/update/view pattern as other [Charm Bracelet](https://github.com/charmbracelet) components.

[![License](https://img.shields.io/badge/license-MIT-blue.svg)](https://raw.githubusercontent.com/grafviktor/termview/develop/LICENSE)

## 1. Functional demo ##

This demo represents routing PTY I/O into x/vt emulators, rendering with [bubbletea](https://github.com/charmbracelet/bubbletea) library.

![Two-pane terminal multiplexer demo with focus switching and horizontal/vertical layout toggle](examples/terminal-multiplexer/terminal-multiplexer.gif)

## 2. Installation and usage ##

```bash
go get github.com/grafviktor/termview@v0.4.0
```

```go
import "github.com/grafviktor/termview"

...
term, err := termview.New(
    termview.WithCommand("/bin/bash"),
    termview.WithInitialWidth(80),
    termview.WithInitialHeight(24),
    termview.WithScrollbackSize(10000),
)
```

Also see [terminal-simple](examples/terminal-simple) and [terminal-multiplexer](examples/terminal-multiplexer) for the examples.

## 3. Scrolling ##

Lines that leave the top of the screen are kept in a scrollback buffer, 10000 lines by default. Use `termview.WithScrollbackSize` to change the limit. You can use mouse for scrolling or `shift+pgup`, `shift+pgdown`, `shift+up`, `shift+down` buttons.

## 4. Clipboard ##

TermView emits when user selects a text and then releases the mouse. You can handle it in your app with such code:
```go
case termview.TextSelectedMsg:
    if msg.ID != a.term.ID() {
        return a, nil
    }
    a.status = "Copied to clipboard"
    return a, tea.Batch(
        tea.SetClipboard(msg.Text),
        tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearStatusMsg{} }),
    )
```

## 5. License ##

MIT - see [LICENSE](LICENSE).
