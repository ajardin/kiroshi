package tui

import (
	"encoding/base64"
	"fmt"
	"os"
)

// CopyToClipboard copies text to the system clipboard by emitting an OSC 52
// escape sequence on stdout. Unlike shelling out to pbcopy/xclip, OSC 52 is
// interpreted by the terminal emulator itself, so it works over SSH and inside
// tmux with zero dependencies. The sequence moves no cursor, so it does not
// disturb the Bubble Tea renderer; it is written synchronously from Update
// (the program loop's goroutine), never concurrently with a frame flush.
func CopyToClipboard(text string) error {
	seq := fmt.Sprintf("\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
	if _, err := os.Stdout.WriteString(seq); err != nil {
		return fmt.Errorf("write clipboard sequence: %w", err)
	}
	return nil
}
