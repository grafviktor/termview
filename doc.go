// Package termview embeds a PTY-backed shell in Bubble Tea programs.

// It provides a terminal model backed by a PTY and x/vt emulator.
//
// # KeyPressMsg flow
//
// This describes what happens when a user types a key and how it reaches the
// real terminal process.
//
// 1. Write to the shell
//
//   - User types a character and Bubble Tea generates a tea.KeyPressMsg.
//   - Model.Update forwards the message to x/vt key handling:
//     tw.emu.SendKey(vt.KeyPressEvent(msg)).
//   - x/vt converts the key event into bytes and writes them into the emulator's
//     internal output buffer (via io.WriteString(e.pw, seq)).
//   - This unblocks the writeShell goroutine, which is waiting on tw.emu.Read.
//   - writeShell writes those bytes to the PTY with tw.pty.Write(buf[:n]).
//   - The shell running on the other end of the PTY receives those bytes as
//     stdin.
//
// 2. Read from the shell
//
// This is the continuation of step 1.
//
//   - The shell processes the input and produces output (echo, command result,
//     prompts, etc.).
//   - The shell writes that output back to the PTY.
//   - readShell reads from the PTY and receives the shell output bytes.
//   - A OutputMsg is created and sent back into Update.
//   - The output is written to the emulator and rendered in Bubble Tea.

package termview
